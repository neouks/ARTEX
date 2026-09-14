package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWaitingWorkerCancelAndManualResume(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	defer m.Close()
	task, err := m.CreateTask("worker cancellation", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	s := newAdmissionTestServer(m, nil)
	id, err := task.Store.AddIntent(map[string]any{"summary": "waiting worker"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.applyIntentControl(t.Context(), task, id, "cancel", "")
	if err != nil || !out.CancelledByUser || out.State != "stopped" || out.CancelReason != "用户取消等待运行" {
		t.Fatalf("cancel: %+v %v", out, err)
	}
	if events := task.drainTriggers(); len(events) != 1 || events[0].Kind != "cancelled" {
		t.Fatalf("notification: %+v", events)
	}
	if _, err := s.applyIntentControl(t.Context(), task, id, "cancel", "ignored"); err != nil {
		t.Fatal(err)
	}
	if len(task.drainTriggers()) != 0 {
		t.Fatal("duplicate trigger")
	}
	if in := s.engine.claimNext(task, "worker"); in != nil {
		t.Fatal("cancelled work claimed")
	}
	// Force task admission failure after reopening, then verify rollback.
	s.engine.deleting.Store(task.ID, true)
	if _, err := s.applyIntentControl(t.Context(), task, id, "resume", ""); err == nil {
		t.Fatal("admission unexpectedly succeeded")
	}
	n, _ := task.Store.GetNode(id)
	if !n.UserCancelled() || n.State != "stopped" {
		t.Fatal("rollback lost cancellation")
	}
	s.engine.deleting.Delete(task.ID)
	out, err = s.applyIntentControl(t.Context(), task, id, "resume", "")
	if err != nil || out.State != "open" || out.CancelledByUser {
		t.Fatalf("resume: %+v %v", out, err)
	}
	if _, err := s.applyIntentControl(t.Context(), task, id, "cancel", "again"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.SetPathValue("id", task.ID)
	r.SetPathValue("iid", fmt.Sprint(id))
	w := httptest.NewRecorder()
	s.rerunIntent(w, r)
	if w.Code != 200 {
		t.Fatalf("manual rerun: %d %s", w.Code, w.Body.String())
	}
	n, _ = task.Store.GetNode(id)
	if n.UserCancelled() || n.State != "open" {
		t.Fatal("rerun did not clear lock")
	}

}

func TestCancelWaitsForClaimRegistration(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	defer m.Close()
	task, err := m.CreateTask("registration race", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	s := newAdmissionTestServer(m, nil)
	id, err := task.Store.AddIntent(map[string]any{"summary": "claimed worker"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := task.Store.ClaimIntent(id, "worker"); err != nil || !ok {
		t.Fatal("claim", err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := s.applyIntentControl(context.Background(), task, id, "cancel", "stop")
		finished <- err
	}()
	select {
	case err := <-finished:
		t.Fatalf("returned before registration: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	s.engine.registerWork(id, cancel)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("work not cancelled")
	}
	_, complete := s.engine.detachWork(id)
	complete(transitionIntentState(task.Store, id, "running", "paused"))
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not finish")
	}
	n, _ := task.Store.GetNode(id)
	if n.State != "stopped" || !n.UserCancelled() {
		t.Fatal("cancel lock missing")
	}
}
