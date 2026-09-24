package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	actool "github.com/Autumn-27/norma/tool"
)

func (s *Server) setExecutionMode(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	var req struct {
		Mode string `json:"execution_mode"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || (req.Mode != db.ExecutionManual && req.Mode != db.ExecutionManaged) {
		writeErr(w, 400, "execution_mode must be managed|manual")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除")
		return
	}
	defer s.engine.decInflight(t.ID)
	t.workerControlMu.Lock()
	defer t.workerControlMu.Unlock()
	changed, err := t.Store.SetExecutionMode(req.Mode)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.updateLifecycle(func(st *taskLifecycleState) { st.ExecutionMode = req.Mode })
	if req.Mode == db.ExecutionManual && t.plannerCancel != nil {
		t.plannerCancel()
	}
	if req.Mode == db.ExecutionManual {
		t.drainTriggers() // Stale per-round hints are reconstructed from persistent state on resume.
		state := t.lifecycleSnapshot()
		if changed && s.engine.Started(t.ID) && !state.Paused && !state.Queued && !s.engine.IsPaused(t.ID) && !isTerminalStatus(state.Status) {
			s.engine.stampFirstRun(t) // Manual waiting still observes the task timeout.
		}
	}
	if changed {
		s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: "用户切换执行模式：" + map[string]string{"managed": "托管", "manual": "手工；Planner 自动规划已停止，未领取意图等待选择下发"}[req.Mode]})
		t.Notify()
		t.wakeWorkers()
	}
	writeJSON(w, 200, taskDTO(t, s.resolvedTaskStatus(t)))
}

func (s *Server) dispatchIntents(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	var req struct {
		IDs []string `json:"intent_ids"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.IDs) == 0 || len(req.IDs) > 50 {
		writeErr(w, 400, "intent_ids must contain 1..50 IDs")
		return
	}
	ids := make([]int64, 0, len(req.IDs))
	for _, raw := range req.IDs {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, 400, "invalid intent id")
			return
		}
		ids = append(ids, id)
	}
	results, err := s.dispatchTaskIntents(r.Context(), t, ids)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

func (s *Server) dispatchTaskIntents(ctx context.Context, t *Task, ids []int64) ([]agent.IntentDispatchResult, error) {
	if len(ids) == 0 || len(ids) > 50 {
		return nil, fmt.Errorf("一次下发 1..50 个意图")
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("意图 ID 必须为正整数")
		}
	}
	if !s.engine.beginTaskOperation(t.ID) {
		return nil, fmt.Errorf("任务正在删除")
	}
	defer s.engine.decInflight(t.ID)
	t.workerControlMu.Lock()
	defer t.workerControlMu.Unlock()
	return s.dispatchTaskIntentsLocked(ctx, t, ids)
}

func (s *Server) dispatchTaskIntentsLocked(ctx context.Context, t *Task, ids []int64) (results []agent.IntentDispatchResult, resultErr error) {
	defer func() {
		if resultErr == nil {
			return
		}
		for _, id := range agent.CreatedDispatchIntents(ctx) {
			n, err := t.Store.GetNode(id)
			if err == nil && n != nil && n.State == "open" && n.DispatchRequested() {
				if err = t.Store.SetIntentDispatch(id, false); err != nil {
					resultErr = fmt.Errorf("%w; 撤回新意图下发失败: %v", resultErr, err)
				}
			}
		}
	}()
	ri := agent.RunInfoFrom(ctx)
	if ri.AgentKey == "mainagent" && ri.IntentID == 0 && ri.TaskID > 0 && strconv.FormatInt(ri.TaskID, 10) == t.ID {
		return s.dispatchMainIntentsLocked(ctx, t, ids)
	}
	if len(ids) == 0 || len(ids) > 50 {
		return nil, fmt.Errorf("一次下发 1..50 个意图")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	seen := map[int64]bool{}
	var changed []int64
	var out []agent.IntentDispatchResult
	needsAdmission := false
	created := map[int64]bool{}
	for _, id := range agent.CreatedDispatchIntents(ctx) {
		created[id] = true
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		result := agent.IntentDispatchResult{ID: id, Status: "rejected"}
		n, err := t.Store.GetNode(id)
		switch {
		case err != nil:
			result.Error = err.Error()
		case n == nil || n.Kind != db.KindIntent:
			result.Error = "意图不存在或不属于当前任务"
		case n.UserCancelled():
			result.Error = "意图已取消，请使用重跑入口"
		case n.State == "running":
			result.Status = "running"
		case n.State != "open":
			result.Error = "仅待执行意图可下发；终态意图请重跑"
		default:
			assetIDs, accessErr := agent.IntentAssetIDs(t.Store, n)
			if accessErr == nil && s.m.assets != nil {
				accessErr = s.m.assets.ValidateTaskAssetsApproved(taskID, assetIDs)
			}
			if accessErr == nil && s.m.assets != nil {
				accessErr = s.m.assets.ValidateTaskHostsApproved(taskID, guard.CollectTargetHosts(n.Payload))
			}
			if accessErr != nil {
				result.Error = accessErr.Error()
				break
			}
			if n.DispatchRequested() {
				result.Status = "already_dispatched"
				if created[id] {
					changed = append(changed, id)
					result.Status = "dispatched"
				}
				needsAdmission = true
				break
			}
			if err = t.Store.SetIntentDispatch(id, true); err != nil {
				result.Error = err.Error()
				break
			}
			changed = append(changed, id)
			needsAdmission = true
			result.Status = "dispatched"
		}
		if created[id] && result.Status == "rejected" {
			if rollbackErr := t.Store.SetIntentDispatch(id, false); rollbackErr != nil {
				return nil, rollbackErr
			}
		}
		out = append(out, result)
	}
	if needsAdmission {
		if _, err := s.admitTask(t, "resume"); err != nil {
			for _, id := range changed {
				if rollbackErr := t.Store.SetIntentDispatch(id, false); rollbackErr != nil {
					return nil, fmt.Errorf("准入失败: %v；撤回下发失败: %w", err, rollbackErr)
				}
			}
			return nil, err
		}
		t.wakeWorkers()
	}
	if len(changed) > 0 {
		s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: fmt.Sprintf("用户下发意图：%v", changed)})
	}
	return out, nil
}

func (s *Server) finishTask(t *Task) (taskControlResult, error) {
	if t == nil {
		return taskControlResult{}, fmt.Errorf("task not found")
	}
	out := taskControlResult{ID: t.ID}
	t.workerControlMu.Lock()
	defer t.workerControlMu.Unlock()
	s.concMu.Lock()
	defer s.concMu.Unlock()
	if !s.engine.beginTaskOperation(t.ID) {
		return out, fmt.Errorf("任务正在删除")
	}
	defer s.engine.decInflight(t.ID)
	s.m.taskStateMu.Lock()
	defer s.m.taskStateMu.Unlock()
	if isTerminalStatus(t.lifecycleSnapshot().Status) {
		out.Status = t.lifecycleSnapshot().Status
		return out, nil
	}
	if err := t.Store.FinishByUser(); err != nil {
		return out, err
	}
	t.updateLifecycle(func(st *taskLifecycleState) {
		st.Status = "done"
		st.CompletedAt = time.Now().Unix()
		st.Paused = false
		st.Queued = false
		st.QueueMode = ""
		st.QueuedAt = 0
	})
	// Persist termination before cancelling so workers settle to stopped, preserving products.
	s.engine.Pause(t.ID, agent.AbortTaskFinishedByUser)
	s.cancelTaskChat(t.ID, agent.AbortChatStoppedByUser)
	s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: "用户结束任务；保留意图、会话和已有产出"})
	go s.reconcileConcurrency()
	out.Status = "done"
	return out, nil
}

func (s *Server) intentDispatchContext(ctx context.Context, t *Task) context.Context {
	ctx = agent.WithIntentDispatcher(ctx, func(call context.Context, ids []int64) ([]agent.IntentDispatchResult, error) {
		return s.dispatchTaskIntents(call, t, ids)
	})
	return agent.WithIntentSubmission(ctx, func(call context.Context, run func(context.Context) (actool.Result, error)) (actool.Result, error) {
		if !s.engine.beginTaskOperation(t.ID) {
			return actool.Errorf("任务正在删除"), nil
		}
		defer s.engine.decInflight(t.ID)
		t.workerControlMu.Lock()
		defer t.workerControlMu.Unlock()
		locked := agent.WithIntentDispatcher(call, func(inner context.Context, ids []int64) ([]agent.IntentDispatchResult, error) {
			return s.dispatchTaskIntentsLocked(inner, t, ids)
		})
		return run(locked)
	})
}

// Main Agent permission is published only after admission, so failed admission
// cannot leave a pending-asset exception behind. The caller holds workerControlMu.
func (s *Server) dispatchMainIntentsLocked(ctx context.Context, t *Task, ids []int64) ([]agent.IntentDispatchResult, error) {
	if len(ids) == 0 || len(ids) > 50 {
		return nil, fmt.Errorf("一次下发 1..50 个意图")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	out := []agent.IntentDispatchResult{}
	seen := map[int64]bool{}
	created := map[int64]bool{}
	for _, id := range agent.CreatedDispatchIntents(ctx) {
		created[id] = true
	}
	candidates := []int{}
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("意图 ID 必须为正整数")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		row := agent.IntentDispatchResult{ID: id, Status: "rejected"}
		n, err := t.Store.GetNode(id)
		switch {
		case err != nil:
			row.Error = err.Error()
		case n == nil || n.Kind != db.KindIntent:
			row.Error = "意图不存在或不属于当前任务"
		case n.UserCancelled():
			row.Error = "意图已取消，请使用重跑入口"
		case n.State == "running":
			row.Status = "running"
		case n.State != "open":
			row.Error = "仅待执行意图可下发；终态意图请重跑"
		default:
			assets, e := agent.IntentAssetIDs(t.Store, n)
			if e == nil && s.m.assets != nil {
				e = s.m.assets.ValidateWorkerAssets(taskID, assets)
			}
			if e == nil && s.m.assets != nil {
				e = s.m.assets.ValidateWorkerHosts(taskID, guard.CollectTargetHosts(n.Payload))
			}
			if e == nil && s.m.assets != nil {
				access, queryErr := s.m.assets.WithExecutionRead().TaskNodeAccess(taskID, []int64{id})
				e = queryErr
				if e == nil && !access[id].CanRead {
					e = fmt.Errorf("意图血缘含不可用资产")
				}
			}
			if e != nil {
				row.Error = e.Error()
			} else {
				row.Status = "dispatched"
				if n.DispatchRequested() && n.AllowsPendingAssets() && !created[id] {
					row.Status = "already_dispatched"
				}
				candidates = append(candidates, len(out))
			}
		}
		out = append(out, row)
	}
	if len(candidates) == 0 {
		return out, nil
	}
	if _, err := s.admitTask(t, "resume"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	granted := []int64{}
	for _, i := range candidates {
		if err := t.Store.GrantMainIntentDispatch(out[i].ID); err != nil {
			out[i].Status = "rejected"
			out[i].Error = err.Error()
			continue
		}
		if out[i].Status == "dispatched" {
			granted = append(granted, out[i].ID)
		}
	}
	if len(granted) > 0 {
		s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: fmt.Sprintf("主 Agent 下发意图（允许待审批资产，仍遵守封禁与撤回）：%v", granted)})
	}
	t.wakeWorkers()
	return out, nil
}
