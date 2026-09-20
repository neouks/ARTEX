package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/agent"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/sidequestion"
)

func (s *Server) workerQueue(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除")
		return
	}
	defer s.engine.decInflight(t.ID)
	t.workerControlMu.Lock()
	defer t.workerControlMu.Unlock()
	var q db.WorkerQueue
	var err error
	if r.Method == "POST" {
		var req struct {
			ID      int64   `json:"id,string"`
			Before  *string `json:"before_id"`
			Version *int64  `json:"version"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ID <= 0 || req.Version == nil {
			writeErr(w, 400, "bad queue move")
			return
		}
		var before *int64
		if req.Before != nil {
			v, e := strconv.ParseInt(*req.Before, 10, 64)
			if e != nil || v <= 0 {
				writeErr(w, 400, "bad before_id")
				return
			}
			before = &v
		}
		q, err = t.Store.MoveWorker(req.ID, before, *req.Version)
	} else {
		q, err = t.Store.WorkerQueue()
	}
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	if r.Method == "POST" {
		t.Notify()
	}
	writeJSON(w, 200, map[string]any{"items": taskNodeDTOs(q.Items), "manual": q.Manual, "version": q.Version, "execution_mode": q.ExecutionMode})
}
func (s *Server) deleteWorker(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad intent id")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除")
		return
	}
	defer s.engine.decInflight(t.ID)
	t.workerControlMu.Lock()
	defer t.workerControlMu.Unlock()
	s.engine.workMu.Lock()
	live := s.engine.work[id] != nil
	s.engine.workMu.Unlock()
	if live {
		writeErr(w, 409, "Worker 已被领取，请刷新列表")
		return
	}
	node, err := t.Store.GetNode(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node != nil && (node.State != "open" && !(node.State == "stopped" && node.UserCancelled())) {
		writeErr(w, 409, "仅等待运行或用户已取消的 Worker 可以删除")
		return
	}
	// Prevent new side requests while draining existing ones, then erase their snapshots.
	if s.side != nil {
		s.side.commands.Lock()
		defer s.side.commands.Unlock()
		done := s.cancelSideWhere(func(p sidequestion.Parent) bool { return p.ExplorationID == t.Store.ID() && p.IntentID == id })
		for _, ch := range done {
			select {
			case <-ch:
			case <-r.Context().Done():
				writeErr(w, 409, "等待旁路会话停止失败，请重试")
				return
			}
		}

	}
	var fileStage *taskFileDeleteStage
	defer func() {
		if fileStage != nil {
			_ = fileStage.rollback()
		}
	}()
	changed, err := t.Store.DeleteWorkerWithCleanup(id, func() error {
		var stageErr error
		fileStage, stageErr = s.stageWorkerFiles(t, id)
		return stageErr
	})
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	if s.side != nil {
		s.side.mu.Lock()
		for key, snap := range s.side.latest {
			if snap.Parent.ExplorationID == t.Store.ID() && snap.Parent.IntentID == id {
				delete(s.side.latest, key)
				delete(s.side.pending, key)
				delete(s.side.seen, key)
			}
		}
		s.side.mu.Unlock()
	}
	if changed {
		summary := ""
		if node != nil {
			var p struct {
				Summary string `json:"summary"`
			}
			_ = json.Unmarshal(node.Payload, &p)
			summary = p.Summary
		}
		t.NotifyHint([]string{fmt.Sprintf("用户已删除 Worker #%d：%s。原 Worker 及会话已移除，已有产出保留，原 Worker 不可再次开启。", id, summary)})
	}
	if err := fileStage.commit(); err != nil {
		writeErr(w, 500, "Worker 已删除，但会话文件清理失败："+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": strconv.FormatInt(id, 10), "deleted": true})
}

// Stage only this worker's transcript and its own side-question transcripts.
func (s *Server) stageWorkerFiles(t *Task, id int64) (*taskFileDeleteStage, error) {
	sessions := []string{agent.WorkerSessionID(t.Store.ID(), id)}
	rows, err := s.m.pg.Query(`SELECT r.id FROM side_question_requests r JOIN side_question_sessions p ON p.session_key=r.session_key WHERE p.exploration_id=$1 AND p.intent_id=$2`, t.Store.ID(), id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var request string
		if err = rows.Scan(&request); err != nil {
			rows.Close()
			return nil, err
		}
		sessions = append(sessions, fmt.Sprintf("exp%d-btw-%s", t.Store.ID(), request))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	stage := &taskFileDeleteStage{}
	for _, session := range sessions {
		for _, suffix := range []string{".jsonl", ""} {
			source := filepath.Join(s.m.dir, "transcripts", session+suffix)
			if _, err = os.Lstat(source); os.IsNotExist(err) {
				continue
			} else if err != nil {
				stage.rollback()
				return nil, err
			}
			if stage.stageDir == "" {
				parent := filepath.Join(s.m.dir, ".delete-staging")
				if err = os.MkdirAll(parent, 0700); err != nil {
					return nil, err
				}
				stage.stageDir, err = os.MkdirTemp(parent, fmt.Sprintf("worker-%d-%d-", t.Store.ID(), id))
				if err != nil {
					return nil, err
				}
			}
			target := filepath.Join(stage.stageDir, fmt.Sprintf("%d-%s", len(stage.moves), filepath.Base(source)))
			if err = os.Rename(source, target); err != nil {
				return nil, errors.Join(err, stage.rollback())
			}
			stage.moves = append(stage.moves, stagedTaskPath{source: source, staged: target})
		}
	}
	if stage.stageDir == "" {
		stage.done = true
	}
	return stage, nil
}
