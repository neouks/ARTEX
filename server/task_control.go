package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

const maxBatchControlIDs = 100

type taskControlResult struct {
	ID     string `json:"id"`
	Paused bool   `json:"paused"`
	Queued bool   `json:"queued"`
	Status string `json:"status"`
}

type intentControlResult struct {
	CancelledByUser bool              `json:"cancelled_by_user"`
	CancelReason    string            `json:"cancel_reason,omitempty"`
	Queued          bool              `json:"queued,omitempty"`
	ID              int64             `json:"id"`
	State           string            `json:"state"`
	Deleted         *db.IntentCleanup `json:"deleted,omitempty"`
}

// parsedTaskID carries one requested batch id together with whether it parsed.
// Invalid ids are kept rather than dropped so the response can name them.
type parsedTaskID struct {
	id    string
	valid bool
}

// normalizeBatchTaskIDs trims, canonicalizes and de-duplicates the ids of one
// batch request while preserving the caller's order. Shared by every batch
// endpoint so they agree on what counts as a duplicate.
func normalizeBatchTaskIDs(raw []string) []parsedTaskID {
	seen := map[string]bool{}
	seenInvalid := map[string]bool{}
	taskIDs := make([]parsedTaskID, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		id, valid := canonicalTaskID(trimmed)
		if !valid {
			if seenInvalid[trimmed] {
				continue
			}
			seenInvalid[trimmed] = true
			taskIDs = append(taskIDs, parsedTaskID{id: trimmed})
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		taskIDs = append(taskIDs, parsedTaskID{id: id, valid: true})
	}
	return taskIDs
}

type batchControlItem struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Status string `json:"status,omitempty"`
	Queued bool   `json:"queued,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) resumeAdmissionMode(t *Task) string {
	if t == nil {
		return "resume"
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.QueueMode == "bootstrap" {
		return "bootstrap"
	}
	if lifecycle.FirstRunAt == 0 {
		goals, err := t.Store.ListByKind(db.KindGoal, 1)
		if err == nil && len(goals) == 0 {
			return "bootstrap"
		}
	}
	return "resume"
}

// applyTaskControl is shared by the single and batch endpoints. Task resume is
// deliberately limited to paused tasks; reruns and finding follow-ups use
// admitTask directly when they need to revive a terminal task.
func (s *Server) applyTaskControl(t *Task, action string) (taskControlResult, error) {
	return s.applyTaskControlWithCause(t, action, agent.AbortPausedByUser)
}

func (s *Server) applyTaskControlWithCause(t *Task, action string, pauseCause error) (taskControlResult, error) {
	if t == nil {
		return taskControlResult{}, fmt.Errorf("task not found")
	}
	out := taskControlResult{ID: t.ID}
	switch action {
	case "pause":
		s.concMu.Lock()
		defer s.concMu.Unlock()
		current, exists := s.m.Task(t.ID)
		if !exists || current != t || s.engine.IsDeleting(t.ID) {
			return out, fmt.Errorf("任务正在删除，无法控制")
		}
		if !s.engine.beginTaskOperation(t.ID) {
			return out, fmt.Errorf("任务正在删除，无法控制")
		}
		defer s.engine.decInflight(t.ID)
		lifecycle := t.lifecycleSnapshot()
		if isTerminalStatus(lifecycle.Status) {
			return out, fmt.Errorf("终态任务不能执行暂停")
		}
		if lifecycle.Paused {
			return out, fmt.Errorf("任务已经暂停")
		}
		wasQueued := lifecycle.Queued
		wasEnginePaused := s.engine.IsPaused(t.ID)
		if pauseCause == nil {
			pauseCause = agent.AbortPausedByUser
		}
		s.engine.Pause(t.ID, pauseCause)
		if err := s.m.ApplyTaskPause(t.ID); err != nil {
			if !wasEnginePaused && !wasQueued {
				s.engine.Resume(t)
			}
			return out, err
		}
		// Main Agent is independently cancellable. Only cancel its current turn
		// after the persistent pause commits, so a failed control request is fully
		// compensated and does not lose an otherwise valid conversation turn.
		s.cancelTaskChat(t.ID, agent.AbortChatPausedWithTask)
		out.Paused, out.Status = true, "paused"
		go s.reconcileConcurrency()
	case "resume":
		queued, err := s.admitPausedTask(t)
		if err != nil {
			return out, err
		}
		out.Queued = queued
		out.Status = map[bool]string{true: "queued", false: "running"}[queued]
	default:
		return out, fmt.Errorf("action must be pause|resume")
	}
	log.Printf("[task] #%s %s", t.ID, map[string]string{"pause": "已暂停", "resume": "已继续"}[action])
	return out, nil
}

func (s *Server) applyIntentControl(ctx context.Context, t *Task, iid int64, action, reason string) (intentControlResult, error) {
	t.workerControlMu.Lock()
	defer t.workerControlMu.Unlock()
	out := intentControlResult{ID: iid}
	node, err := t.Store.GetNode(iid)
	if err != nil {
		return out, err
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			return out, fmt.Errorf("继承意图为只读，不能控制")
		}
		return out, fmt.Errorf("intent not found")
	}
	if node.Kind != db.KindIntent {
		return out, fmt.Errorf("node is not an intent")
	}
	switch action {
	case "pause":
		if node.State != "running" {
			return out, fmt.Errorf("仅运行中的意图可以暂停")
		}
		if err := s.engine.ControlWork(ctx, iid, "pause"); err != nil {
			return out, err
		}
		out.State = "paused"
	case "resume":
		if node.State == "open" && !node.UserCancelled() {
			out.State = "open"
			return out, nil
		}
		if node.State != "paused" && !(node.State == "stopped" && node.UserCancelled()) {
			return out, fmt.Errorf("仅已暂停或用户已取消的意图可以重新开启")
		}
		changed, err := t.Store.ReopenIntentByUser(iid, node.State)
		if err != nil {
			return out, err
		}
		if !changed {
			return out, db.ErrIntentStateConflict
		}
		queued, err := s.admitTask(t, "resume")
		if err != nil {
			if restoreErr := t.Store.RestoreUserReopen(node); restoreErr != nil {
				return out, fmt.Errorf("%w; restore: %v", err, restoreErr)
			}
			return out, err
		}
		t.Notify()
		out.State, out.Queued = "open", queued
	case "cancel":
		reason = strings.TrimSpace(reason)
		if reason == "" {
			reason = "用户取消等待运行"
			if node.State != "open" {
				reason = "用户取消 Worker"
			}
		}
		// A claimed row can precede registerWork briefly. Wait for registration or
		// settlement without letting another UI action or pool claim race this one.
		waitCtx, cancel := context.WithTimeout(ctx, workControlWaitTimeout)
		defer cancel()
		for {
			if err := waitCtx.Err(); err != nil {
				return out, err
			}
			factID, stopErr := t.Store.StopIntentWithReason(iid, reason, "user")
			if stopErr == nil {
				if factID != 0 {
					s.cancelWorkerSide(t.ID, t.ExpID, iid)
					t.NotifyCancelled(iid, reason)
				}
				current, err := t.Store.GetNode(iid)
				if err != nil {
					return out, err
				}
				out.State, out.CancelledByUser = "stopped", true
				var p struct {
					Reason string `json:"cancel_reason"`
				}
				if current != nil {
					_ = json.Unmarshal(current.Payload, &p)
				}
				out.CancelReason = p.Reason
				break
			}
			if !errors.Is(stopErr, db.ErrIntentStateConflict) {
				return out, stopErr
			}
			current, err := t.Store.GetNode(iid)
			if err != nil {
				return out, err
			}
			if current != nil && (current.State == "paused" || current.State == "open" || (current.State == "stopped" && current.UserCancelled())) {
				continue
			}
			if current == nil || current.State != "running" {
				return out, stopErr
			}
			if err := s.engine.ControlWork(waitCtx, iid, "cancel"); err != nil {
				if !errors.Is(err, errWorkControlConflict) {
					return out, err
				}
				select {
				case <-waitCtx.Done():
					return out, waitCtx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
	default:
		return out, fmt.Errorf("action must be pause|resume|cancel")
	}
	return out, nil
}

func (s *Server) controlTasksBatch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req struct {
		TaskIDs []string `json:"task_ids"`
		Action  string   `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.Action != "pause" && req.Action != "resume" {
		writeErr(w, 400, "action must be pause|resume")
		return
	}
	taskIDs := normalizeBatchTaskIDs(req.TaskIDs)
	if len(taskIDs) == 0 || len(taskIDs) > maxBatchControlIDs {
		writeErr(w, 400, fmt.Sprintf("task_ids 数量必须为 1-%d", maxBatchControlIDs))
		return
	}
	items := make([]batchControlItem, 0, len(taskIDs))
	for _, parsed := range taskIDs {
		item := batchControlItem{ID: parsed.id}
		if !parsed.valid {
			item.Error = "bad task id"
			items = append(items, item)
			continue
		}
		t, ok := s.m.Task(parsed.id)
		if !ok {
			item.Error = "task not found"
			items = append(items, item)
			continue
		}
		result, err := s.applyTaskControl(t, req.Action)
		if err != nil {
			item.Error = err.Error()
		} else {
			item.OK, item.Status, item.Queued = true, result.Status, result.Queued
		}
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
