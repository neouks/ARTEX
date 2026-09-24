package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func modeServer(t *testing.T) (*Server, *Task) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("manual mode", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.DeleteTask(task.ID, DeleteTaskOptions{}); m.Close() })
	return newAdmissionTestServer(m, nil), task
}

func TestManualModeCancelsOnlyPlannerAndSuppressesWakeups(t *testing.T) {
	s, task := modeServer(t)
	workerCtx := s.engine.execContextFor(t.Context(), task.ID)
	plannerCtx, release, allowed := task.beginPlannerRound(workerCtx)
	if !allowed {
		t.Fatal("managed Planner was not admitted")
	}
	defer release()
	r := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"execution_mode":"manual"}`))
	r.SetPathValue("id", task.ID)
	w := httptest.NewRecorder()
	s.setExecutionMode(w, r)
	if w.Code != 200 || plannerCtx.Err() == nil || workerCtx.Err() != nil {
		t.Fatalf("mode switch: code=%d planner=%v worker=%v", w.Code, plannerCtx.Err(), workerCtx.Err())
	}
	if _, _, allowed := task.beginPlannerRound(workerCtx); allowed {
		t.Fatal("manual mode admitted another Planner round")
	}
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	persisted, err := s.m.pg.GetTask(taskID)
	if err != nil || persisted == nil {
		t.Fatal(persisted, err)
	}
	restored := taskFromPG(persisted, task.Store, nil)
	if _, _, allowed := restored.beginPlannerRound(t.Context()); allowed {
		t.Fatal("restored manual task admitted Planner")
	}
	loopCtx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.engine.plannerLoop(loopCtx, task); close(done) }()
	for range 50 {
		task.NotifyDone(1)
	}
	deadline := time.After(time.Second)
	for task.hasPendingTriggers() {
		select {
		case <-deadline:
			t.Fatal("manual Planner retained wakeup events")
		case <-time.After(time.Millisecond):
		}
	}
	if _, active := s.engine.plannerActive.Load(task.ID); active {
		t.Fatal("manual Planner invoked the model")
	}
	if _, round := s.engine.plannerRound.Load(task.ID); round {
		t.Fatal("manual Planner created a round marker")
	}
	cancel()
	<-done
	r = httptest.NewRequest("PATCH", "/", strings.NewReader(`{"execution_mode":"managed"}`))
	r.SetPathValue("id", task.ID)
	w = httptest.NewRecorder()
	s.setExecutionMode(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if resumed, done, allowed := task.beginPlannerRound(workerCtx); !allowed {
		t.Fatal("managed mode did not restore Planner admission")
	} else {
		done()
		if resumed.Err() == nil {
			t.Fatal("released Planner context stayed live")
		}
	}
}

func TestManualModeSkipsTerminalPlannerOnTimeout(t *testing.T) {
	s, task := modeServer(t)
	if _, err := task.Store.SetExecutionMode(db.ExecutionManual); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.ExecutionMode = db.ExecutionManual })
	if met := s.engine.runFinalPlannerRound(t.Context(), task); met {
		t.Fatal("manual timeout called terminal Planner")
	}
	s.engine.settleTask(t.Context(), task)
	if status := s.m.TaskStatus(task.ID); status != "timeout" {
		t.Fatalf("manual timeout ended as %q", status)
	}
}

func TestManualModeStartsTimeoutWithoutPlanner(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("manual timeout", "goal", nil, 60, 0)
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { m.DeleteTask(task.ID, DeleteTaskOptions{}); m.Close() })
	if _, err := task.Store.SetExecutionMode(db.ExecutionManual); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.ExecutionMode = db.ExecutionManual })
	e := NewEngine(m)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.Run(ctx, task)
	state := task.lifecycleSnapshot()
	if state.FirstRunAt == 0 || state.DeadlineAt <= state.FirstRunAt {
		t.Fatalf("manual task timeout was not started: first=%d deadline=%d", state.FirstRunAt, state.DeadlineAt)
	}
	if _, started := e.plannerRound.Load(task.ID); started {
		t.Fatal("starting a manual task created a Planner round")
	}
}

func TestManualModeResumeStartsTimeout(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("paused manual timeout", "goal", nil, 60, 0)
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { m.DeleteTask(task.ID, DeleteTaskOptions{}); m.Close() })
	if _, err := task.Store.SetExecutionMode(db.ExecutionManual); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(state *taskLifecycleState) {
		state.ExecutionMode = db.ExecutionManual
		state.Paused = true
	})
	e := NewEngine(m)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.Run(ctx, task)
	if task.lifecycleSnapshot().FirstRunAt != 0 {
		t.Fatal("paused manual task started timeout")
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = false })
	e.Run(ctx, task)
	if task.lifecycleSnapshot().DeadlineAt == 0 {
		t.Fatal("resumed manual task did not start timeout")
	}
}
func TestManualDispatchAndFinish(t *testing.T) {
	s, task := modeServer(t)
	r := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"execution_mode":"manual"}`))
	r.SetPathValue("id", task.ID)
	w := httptest.NewRecorder()
	s.setExecutionMode(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, _ := task.Store.AddIntent(map[string]any{"summary": "one"}, 1, nil, "planner")
	b, _ := task.Store.AddIntent(map[string]any{"summary": "two"}, 1, nil, "planner")
	if won, err := s.engine.completeAutomatically(task); won || err != nil {
		t.Fatal("manual auto finished", won, err)
	}
	results, err := s.dispatchTaskIntents(context.Background(), task, []int64{a, b + 1000000, a})
	if err != nil || len(results) != 2 || results[0].Status != "dispatched" || results[1].Status != "rejected" {
		t.Fatal(results, err)
	}
	n, _ := task.Store.GetNode(a)
	if !n.DispatchRequested() {
		t.Fatal("release not persisted")
	}
	n, _ = task.Store.GetNode(b)
	if n.DispatchRequested() {
		t.Fatal("unselected released")
	}
	results, err = s.dispatchTaskIntents(context.Background(), task, []int64{a})
	if err != nil || results[0].Status != "already_dispatched" {
		t.Fatal(results, err)
	}
	active := s.engine.execContextFor(context.Background(), task.ID)
	out, err := s.finishTask(task)
	if err != nil || out.Status != "done" {
		t.Fatal(out, err)
	}
	if active.Err() == nil || context.Cause(active) != agent.AbortTaskFinishedByUser {
		t.Fatal("finish did not cancel active run", context.Cause(active))
	}
	if !s.engine.IsPaused(task.ID) || task.lifecycleSnapshot().Queued {
		t.Fatal("finish lacks barrier")
	}
	if n := s.engine.claimNext(task, "test"); n != nil {
		t.Fatal("claimed after finish")
	}
	nodes, _ := task.Store.ListByKind(db.KindIntent, 10)
	if len(nodes) != 2 {
		t.Fatal("finish deleted products")
	}
	dto := taskDTO(task, "done")
	if dto.ExecutionMode != "manual" || dto.CompletedAt == "" {
		t.Fatal(dto)
	}
	goals, _ := task.Store.ListByKind(db.KindGoal, 10)
	for _, goal := range goals {
		if goal.State == "met" {
			t.Fatal("finish changed goal")
		}
	}
}
func TestManualDispatchRequiresLocalOpenIntent(t *testing.T) {
	s, task := modeServer(t)
	_, other := modeServer(t)
	id, _ := other.Store.AddIntent(map[string]any{"summary": "other"}, 1, nil, "planner")
	results, err := s.dispatchTaskIntents(t.Context(), task, []int64{id})
	if err != nil || results[0].Status != "rejected" {
		t.Fatal(results, err)
	}
	r := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"execution_mode":"invalid"}`))
	r.SetPathValue("id", task.ID)
	w := httptest.NewRecorder()
	s.setExecutionMode(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	raw, _ := json.Marshal(taskDTO(task, "running"))
	if !strings.Contains(string(raw), `"execution_mode":"managed"`) {
		t.Fatal(string(raw))
	}
}

func TestManualMainAgentRuntimeDispatchAndRollback(t *testing.T) {
	s, task := modeServer(t)
	task.Store.SetExecutionMode("manual")
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	ts := agent.NewToolSet(task.Store, "human")
	ts.SetTaskID(taskID)
	var add actool.CoreTool
	for _, tool := range ts.MainAgentTools() {
		if tool.Name() == "add_intent" {
			add = tool
		}
	}
	ctx := s.intentDispatchContext(agent.WithRunInfo(t.Context(), agent.RunInfo{AgentKey: "mainagent", TaskID: taskID}), task)
	for i := 0; i < 2; i++ {
		res, err := add.Call(ctx, json.RawMessage(`{"summary":"main direct test"}`), nil)
		if err != nil || res.IsError || !strings.Contains(res.Flatten(), "dispatched") {
			t.Fatal(res, err)
		}
	}
	q, err := task.Store.WorkerQueue()
	if err != nil || len(q.Items) != 1 || !q.Items[0].DispatchRequested() {
		t.Fatal(q, err)
	}
	// A delete barrier can win after the outer operation started but before task admission.
	task.Store.SetIntentDispatch(q.Items[0].ID, false)
	s.engine.deleting.Store(task.ID, true)
	task.workerControlMu.Lock()
	_, err = s.dispatchTaskIntentsLocked(t.Context(), task, []int64{q.Items[0].ID})
	task.workerControlMu.Unlock()
	s.engine.deleting.Delete(task.ID)
	if err == nil {
		t.Fatal("admission should fail")
	}
	n, _ := task.Store.GetNode(q.Items[0].ID)
	if n.DispatchRequested() {
		t.Fatal("failed admission left execution permission")
	}
}

func TestManualDispatchAssetRevocationAndPartialSuccess(t *testing.T) {
	s, task := modeServer(t)
	task.Store.SetExecutionMode("manual")
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	asset, err := s.m.assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: fmt.Sprintf("mode-%d.test", taskID), TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	id, err := task.Store.AddIntent(map[string]any{"summary": "asset test"}, 1, []int64{asset}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	free, err := task.Store.AddIntent(map[string]any{"summary": "local notes"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.m.assets.RevokeTaskAssets(taskID, []int64{asset}, "test", "revoked before dispatch"); err != nil {
		t.Fatal(err)
	}
	result, err := s.dispatchTaskIntents(t.Context(), task, []int64{id, free})
	if err != nil || result[0].Status != "rejected" || result[1].Status != "dispatched" {
		t.Fatal(result, err)
	}
	if err = s.m.assets.ApproveTaskAssets(taskID, []int64{asset}, "test", ""); err != nil {
		t.Fatal(err)
	}
	result, err = s.dispatchTaskIntents(t.Context(), task, []int64{id})
	if err != nil || result[0].Status != "dispatched" {
		t.Fatal(result, err)
	}
	if err = s.m.assets.RevokeTaskAssets(taskID, []int64{asset}, "test", "revoked after dispatch"); err != nil {
		t.Fatal(err)
	}
	if won, err := task.Store.ClaimIntent(id, "worker"); err != nil || won {
		t.Fatal("revoked asset claimed", won, err)
	}
	if err := task.Store.FinishByUser(); err != nil {
		t.Fatal(err)
	}
	if won, err := task.Store.ClaimIntent(free, "worker"); err != nil || won {
		t.Fatal("finished task claimed", won, err)
	}
}

func TestManualExplicitResumeAndBatchRerun(t *testing.T) {
	s, task := modeServer(t)
	if _, err := task.Store.SetExecutionMode(db.ExecutionManual); err != nil {
		t.Fatal(err)
	}
	a, err := task.Store.AddIntent(map[string]any{"summary": "resume pending"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyIntentControl(t.Context(), task, a, "resume", ""); err != nil {
		t.Fatal(err)
	}
	n, err := task.Store.GetNode(a)
	if err != nil || !n.DispatchRequested() {
		t.Fatal(n, err)
	}
	b, err := task.Store.AddIntent(map[string]any{"summary": "legacy blocked", "dispatch_requested": false}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(b, "blocked"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.SetPathValue("id", task.ID)
	w := httptest.NewRecorder()
	s.rerunBlocked(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	n, err = task.Store.GetNode(b)
	if err != nil || n.State != "open" || !n.DispatchRequested() {
		t.Fatal(n, err)
	}
}
