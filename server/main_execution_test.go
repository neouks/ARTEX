package server

import (
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
	"strconv"
	"testing"
)

func TestMainPendingDispatchAndAdmissionRollback(t *testing.T) {
	s, task := modeServer(t)
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	if _, e := task.Store.SetExecutionMode(db.ExecutionManual); e != nil {
		t.Fatal(e)
	}
	if e := s.m.pg.SetAssetApprovalTemplate(taskID, "explicit_targets"); e != nil {
		t.Fatal(e)
	}
	a, e := s.m.assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: fmt.Sprintf("main-dispatch-%d.test", taskID), TaskID: taskID, AgentDiscovered: true})
	if e != nil {
		t.Fatal(e)
	}
	ts := agent.NewToolSet(task.Store, "human")
	ts.SetTaskID(taskID)
	ts.SetAssetStore(s.m.assets, s.m.assets.Companies())
	var add actool.CoreTool
	for _, tool := range ts.MainAgentTools() {
		if tool.Name() == "add_intent" {
			add = tool
		}
	}
	ctx := s.intentDispatchContext(agent.WithRunInfo(t.Context(), agent.RunInfo{AgentKey: "mainagent", TaskID: taskID}), task)
	input := json.RawMessage(fmt.Sprintf(`{"summary":"main pending test","asset_ids":[%d]}`, a))
	for range 2 {
		r, e := add.Call(ctx, input, nil)
		if e != nil || r.IsError {
			t.Fatal(r, e)
		}
	}
	queue, e := task.Store.WorkerQueue()
	if e != nil || len(queue.Items) != 1 || !queue.Items[0].AllowsPendingAssets() || !queue.Items[0].DispatchRequested() {
		t.Fatalf("queue %+v %v", queue, e)
	}
	if e := s.m.assets.ValidateTaskAssetsApproved(taskID, []int64{a}); e == nil {
		t.Fatal("asset auto-approved")
	}
	other, e := task.Store.AddIntent(map[string]any{"summary": "existing pending"}, 1, []int64{a}, "planner")
	if e != nil {
		t.Fatal(e)
	}
	out, e := s.dispatchTaskIntents(t.Context(), task, []int64{other})
	if e != nil || out[0].Status != "rejected" {
		t.Fatal("HTTP implicitly gained exemption", out, e)
	}
	out, e = s.dispatchTaskIntents(ctx, task, []int64{other, 999999999})
	if e != nil || out[0].Status != "dispatched" || out[1].Status != "rejected" {
		t.Fatal(out, e)
	}
	failed, e := task.Store.AddIntent(map[string]any{"summary": "fail admission"}, 1, []int64{a}, "planner")
	if e != nil {
		t.Fatal(e)
	}
	s.engine.deleting.Store(task.ID, true)
	task.workerControlMu.Lock()
	_, e = s.dispatchTaskIntentsLocked(ctx, task, []int64{failed})
	task.workerControlMu.Unlock()
	s.engine.deleting.Delete(task.ID)
	if e == nil {
		t.Fatal("expected admission failure")
	}
	n, _ := task.Store.GetNode(failed)
	if n.AllowsPendingAssets() || n.DispatchRequested() {
		t.Fatal("failed admission left permission")
	}
	if e := s.m.assets.RevokeTaskAssets(taskID, []int64{a}, "user", ""); e != nil {
		t.Fatal(e)
	}
	out, e = s.dispatchTaskIntents(ctx, task, []int64{failed})
	if e != nil || out[0].Status != "rejected" {
		t.Fatal("revoked dispatch allowed", out, e)
	}
}
