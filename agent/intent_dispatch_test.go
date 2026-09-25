package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestMainIntentSubmissionAtomicReleaseAndDuplicate(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("main dispatch", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	store := d.Exploration(task.ExplorationID)
	store.SetExecutionMode("manual")
	ts := NewToolSet(store, "human")
	ts.SetTaskID(task.ID)
	ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, AgentKey: "mainagent"})
	creates, dispatches := 0, 0
	ctx = WithIntentDispatcher(ctx, func(c context.Context, ids []int64) ([]IntentDispatchResult, error) {
		dispatches++
		var out []IntentDispatchResult
		for _, id := range ids {
			n, e := store.GetNode(id)
			if e != nil {
				t.Fatal(e)
			}
			if len(CreatedDispatchIntents(c)) > 0 {
				creates++
				if !n.DispatchRequested() {
					t.Fatal("new main intent was committed without its dispatch marker")
				}
			}
			if e = store.SetIntentDispatch(id, true); e != nil {
				t.Fatal(e)
			}
			out = append(out, IntentDispatchResult{ID: id, Status: "dispatched"})
		}
		return out, nil
	})
	ctx = WithIntentSubmission(ctx, func(c context.Context, run func(context.Context) (actool.Result, error)) (actool.Result, error) {
		return run(c)
	})
	in := json.RawMessage(`{"summary":"review local behavior"}`)
	for i := 0; i < 2; i++ {
		res, err := ts.addIntent().Call(ctx, in, nil)
		if err != nil || res.IsError || !strings.Contains(res.Flatten(), "dispatched") {
			t.Fatal(res, err)
		}
	}
	nodes, err := store.Frontier(10)
	if err != nil || len(nodes) != 1 || creates != 1 || dispatches != 2 {
		t.Fatal(nodes, creates, dispatches, err)
	}
}
func TestIntentDispatchCannotBeUsedByPlannerOrTaskless(t *testing.T) {
	ts := NewToolSet(nil, "planner")
	ts.SetTaskID(1)
	called := false
	ctx := WithIntentDispatcher(t.Context(), func(context.Context, []int64) ([]IntentDispatchResult, error) { called = true; return nil, nil })
	for _, role := range []string{"planner", "worker", "mainagent"} {
		taskID := int64(1)
		if role == "mainagent" {
			taskID = 0
		}
		res, err := ts.dispatchIntents().Call(WithRunInfo(ctx, RunInfo{TaskID: taskID, AgentKey: role}), json.RawMessage(`{"intent_ids":[1]}`), nil)
		if err != nil || !res.IsError || called {
			t.Fatal(role, res, err, called)
		}
	}
}

func TestPlannerToolCannotCreateIntentInManualMode(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("manual planner fence", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	store := d.Exploration(task.ExplorationID)
	if _, err := store.SetExecutionMode("manual"); err != nil {
		t.Fatal(err)
	}
	ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, AgentKey: "planner"})
	if met, _, err := new(Planner).Plan(ctx, task.ID, nil, nil, store, "goal", nil, nil); err != nil || met {
		t.Fatal("manual Planner started", met, err)
	}
	tools := NewToolSet(store, "planner")
	tools.SetTaskID(task.ID)
	result, err := tools.addIntent().Call(ctx, json.RawMessage(`{"summary":"late planner direction"}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Flatten(), "手工模式") {
		t.Fatal(result, err)
	}
	if nodes, err := store.Frontier(10); err != nil || len(nodes) != 0 {
		t.Fatal("planner created a manual intent", nodes, err)
	}
}
