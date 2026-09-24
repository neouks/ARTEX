package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPlannerIntentRejectedAfterManualSwitch(t *testing.T) {
	_, store, _ := executionFixture(t)
	addPlanner := func(summary string) (int64, error) {
		id, _, err := store.AddPlannerIntentDeduplicated(t.Context(), map[string]any{"summary": summary}, 5, nil, "planner")
		return id, err
	}
	before, err := addPlanner("before switch")
	if err != nil || before <= 0 {
		t.Fatal(before, err)
	}
	oldPlanner, cancelOldPlanner := context.WithCancel(t.Context())
	if _, err := store.SetExecutionMode(ExecutionManual); err != nil {
		t.Fatal(err)
	}
	cancelOldPlanner()
	if _, err := addPlanner("after switch"); !errors.Is(err, ErrPlannerManualMode) {
		t.Fatalf("planner write after switch: %v", err)
	}
	if _, err := addPlanner("before switch"); !errors.Is(err, ErrPlannerManualMode) {
		t.Fatalf("planner duplicate after switch: %v", err)
	}
	if id, err := store.AddIntent(map[string]any{"summary": "main agent action", "dispatch_requested": true}, 5, nil, "mainagent"); err != nil || id <= 0 {
		t.Fatal("main agent was blocked", id, err)
	}
	if _, err := store.SetExecutionMode(ExecutionManaged); err != nil {
		t.Fatal(err)
	}
	if id, err := addPlanner("after managed resume"); err != nil || id <= 0 {
		t.Fatal(id, err)
	}
	if _, _, err := store.AddPlannerIntentDeduplicated(oldPlanner, map[string]any{"summary": "stale after quick toggle"}, 5, nil, "planner"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled old Planner wrote after managed resume: %v", err)
	}
}

func TestPlannerIntentModeSwitchRace(t *testing.T) {
	_, store, _ := executionFixture(t)
	for i := range 20 {
		if _, err := store.SetExecutionMode(ExecutionManaged); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var id int64
		var addErr, modeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			id, _, addErr = store.AddPlannerIntentDeduplicated(t.Context(), map[string]any{"summary": fmt.Sprintf("race-%d", i)}, 5, nil, "planner")
		}()
		go func() {
			defer wg.Done()
			<-start
			_, modeErr = store.SetExecutionMode(ExecutionManual)
		}()
		close(start)
		wg.Wait()
		if modeErr != nil || (addErr != nil && !errors.Is(addErr, ErrPlannerManualMode)) {
			t.Fatal(modeErr, addErr)
		}
		if addErr == nil {
			node, err := store.GetNode(id)
			if err != nil || node == nil || node.State != "open" || node.DispatchRequested() {
				t.Fatal("pre-switch intent was not held", node, err)
			}
		} else if id != 0 {
			t.Fatal("rejected planner intent was persisted", id)
		}
	}
}

func TestPlannerIntentCancelWhileWaitingForQueueLock(t *testing.T) {
	_, store, _ := executionFixture(t)
	tx, err := store.beginQueue()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := store.AddPlannerIntentDeduplicated(ctx, map[string]any{"summary": "waiting planner"}, 5, nil, "planner")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled queue wait returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Planner remained stuck on the queue lock")
	}
}

func executionFixture(t *testing.T) (*DB, *ExplorationStore, int64) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTask("execution mode", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.DeleteTask(task.ID); d.Close() })
	return d, d.Exploration(task.ExplorationID), task.ID
}
func TestExecutionModeSwitchAndClaim(t *testing.T) {
	d, s, taskID := executionFixture(t)
	a := addQueueIntent(t, s, "running", 10)
	b := addQueueIntent(t, s, "pending", 1)
	if ok, err := s.ClaimIntent(a, "worker"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := s.SetIntentDispatch(b, true); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.SetExecutionMode(ExecutionManual); !changed || err != nil {
		t.Fatal(changed, err)
	}
	n, _ := s.GetNode(a)
	if n.State != "running" {
		t.Fatal("interrupted existing worker")
	}
	n, _ = s.GetNode(b)
	if n.DispatchRequested() {
		t.Fatal("old pending release retained")
	}
	if ok, err := s.ClaimIntent(b, "worker"); ok || err != nil {
		t.Fatal("direct claim bypass", ok, err)
	}
	if n, err := s.ClaimNextWorker("worker", func(*Node) bool { t.Fatal("held intent triggered authorization query"); return true }); n != nil || err != nil {
		t.Fatal("pool claim bypass", n, err)
	}
	if err := s.SetIntentDispatch(b, true); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.SetExecutionMode(ExecutionManual); changed || err != nil {
		t.Fatal("non-idempotent", changed, err)
	}
	n, _ = s.GetNode(b)
	if !n.DispatchRequested() {
		t.Fatal("repeat manual reset release")
	}
	task, err := d.GetTask(taskID)
	if err != nil || task.ExecutionMode != ExecutionManual {
		t.Fatal(task, err)
	}
	if ok, err := d.Exploration(s.ID()).ClaimIntent(b, "worker"); !ok || err != nil {
		t.Fatal("persisted release lost", ok, err)
	}
	c := addQueueIntent(t, s, "future", 1)
	if _, err = s.SetExecutionMode(ExecutionManaged); err != nil {
		t.Fatal(err)
	}
	if n, err = s.ClaimNextWorker("worker", nil); err != nil || n == nil || n.ID != c {
		t.Fatal(n, err)
	}
}
func TestExecutionModeSwitchClaimRace(t *testing.T) {
	_, s, _ := executionFixture(t)
	for i := 0; i < 20; i++ {
		if _, err := s.SetExecutionMode(ExecutionManaged); err != nil {
			t.Fatal(err)
		}
		id := addQueueIntent(t, s, fmt.Sprint(i), 1)
		var wg sync.WaitGroup
		wg.Add(2)
		var claimErr, modeErr error
		go func() { defer wg.Done(); _, claimErr = s.ClaimIntent(id, "race") }()
		go func() { defer wg.Done(); _, modeErr = s.SetExecutionMode(ExecutionManual) }()
		wg.Wait()
		if claimErr != nil || modeErr != nil {
			t.Fatal(claimErr, modeErr)
		}
		node, _ := s.GetNode(id)
		if node.State == "open" {
			if ok, err := s.ClaimIntent(id, "late"); ok || err != nil {
				t.Fatal("claimed after switch", ok, err)
			}
		} else if node.State != "running" {
			t.Fatal(node.State)
		}
	}
}
func TestExecutionModeUserReopenAndIsolation(t *testing.T) {
	_, s, _ := executionFixture(t)
	_, other, _ := executionFixture(t)
	a := addQueueIntent(t, s, "a", 1)
	b := addQueueIntent(t, other, "foreign", 1)
	s.SetExecutionMode(ExecutionManual)
	if err := s.SetIntentDispatch(b, true); err == nil {
		t.Fatal("foreign dispatch")
	}
	if err := s.SetIntentDispatch(a, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIntentState(a, "stopped"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ReopenIntentByUser(a, "stopped"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := s.ClaimIntent(a, "worker"); !ok || err != nil {
		t.Fatal("user rerun held", ok, err)
	}
}

func TestExecutionModeExplicitPausedClaimAndFinish(t *testing.T) {
	_, s, _ := executionFixture(t)
	if _, err := s.SetExecutionMode(ExecutionManual); err != nil {
		t.Fatal(err)
	}
	a := addQueueIntent(t, s, "paused", 1)
	if err := s.SetIntentState(a, "paused"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimPausedIntentByUser(a); !ok || err != nil {
		t.Fatal(ok, err)
	}
	n, err := s.GetNode(a)
	if err != nil || !n.DispatchRequested() {
		t.Fatal(n, err)
	}
	if err := s.SetIntentState(a, "paused"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishByUser(); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimPausedIntentByUser(a); ok || err != nil {
		t.Fatal("claim after finish", ok, err)
	}
}
