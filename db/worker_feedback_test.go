package db

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestWorkerFeedbackDispatchSettlementAndRecovery(t *testing.T) {
	d, s, task := executionFixture(t)
	id := addQueueIntent(t, s, "first", 5)
	for range 2 {
		if err := s.MainDispatchFeedback(t.Context(), id, 2, true); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := s.ClaimIntent(id, "work#1"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	result, err := s.AppendActivity(Activity{NodeID: &id, Worker: "work#1", Kind: "result", Summary: "verified", Detail: "verified final summary"})
	if err != nil {
		t.Fatal(err)
	}
	// Subscribe another segment after result arrival but before settlement. It must
	// still receive the correct result rather than treating it as historical.
	if err = s.MainDispatchFeedback(t.Context(), id, 3, false); err != nil {
		t.Fatal(err)
	}
	if err = s.SetIntentState(id, "done"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetIntentState(id, "done"); err != nil {
		t.Fatal(err)
	}
	rows, err := d.UnpublishedWorkerFeedback(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	for _, r := range rows {
		if r.TaskID != fmt.Sprint(task) || r.Activity.MainSeg == nil || r.Activity.NodeID != nil {
			t.Fatal(r)
		}
		body, err := s.ActivityDetail(r.Activity.ID)
		if err != nil || !strings.Contains(body, "verified final summary") || !strings.Contains(body, fmt.Sprintf("activity=%d", result)) {
			t.Fatal(body, err)
		}
		if err = d.MarkWorkerFeedbackPublished(t.Context(), r.Activity.ID); err != nil {
			t.Fatal(err)
		}
	}
	// A restored store sees persisted results without touching the original audit.
	restored := d.Exploration(s.ID())
	context, err := restored.WorkerFeedbackContext(t.Context(), 2)
	if err != nil || !strings.Contains(context, "verified final summary") {
		t.Fatal(context, err)
	}
	empty, err := restored.WorkerFeedbackContext(t.Context(), 4)
	if err != nil || empty != "" {
		t.Fatal(empty, err)
	}
	raw, err := s.ActivityDetail(result)
	if err != nil || raw != "verified final summary" {
		t.Fatal(raw, err)
	}
	remaining, err := d.UnpublishedWorkerFeedback(t.Context())
	if err != nil || len(remaining) != 0 {
		t.Fatal(remaining, err)
	}
}

func TestWorkerFeedbackPauseRerunFailureAndIsolation(t *testing.T) {
	d, s, _ := executionFixture(t)
	id := addQueueIntent(t, s, "pause", 5)
	if err := s.MainDispatchFeedback(t.Context(), id, 0, true); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimIntent(id, "w"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	s.AppendActivity(Activity{NodeID: &id, Worker: "w", Kind: "result", Detail: "old paused output"})
	if err := s.SetIntentState(id, "paused"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIntentState(id, "open"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimIntent(id, "w"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	s.AppendActivity(Activity{NodeID: &id, Worker: "w", Kind: "text", Detail: "not a final result"})
	if err := s.SetIntentState(id, "blocked"); err != nil {
		t.Fatal(err)
	}
	body, err := s.WorkerFeedbackContext(t.Context(), 0)
	if err != nil || strings.Contains(body, "old paused output") || strings.Contains(body, "not a final result") || !strings.Contains(body, "未产生最终总结") {
		t.Fatal(body, err)
	}
	// Reopen alone (including admission rollback) must not invent a new execution.
	if err := s.SetIntentState(id, "open"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIntentState(id, "blocked"); err != nil {
		t.Fatal(err)
	}
	var count int
	d.QueryRow(`SELECT count(*) FROM worker_feedback WHERE intent_id=$1`, id).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	if err := s.SetIntentState(id, "open"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimIntent(id, "w"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	s.AppendActivity(Activity{NodeID: &id, Worker: "w", Kind: "result", Detail: "new rerun summary"})
	if err := s.SetIntentState(id, "done"); err != nil {
		t.Fatal(err)
	}
	d.QueryRow(`SELECT count(*) FROM worker_feedback WHERE intent_id=$1`, id).Scan(&count)
	if count != 2 {
		t.Fatal(count)
	}
	body, err = s.WorkerFeedbackContext(t.Context(), 0)
	if err != nil || !strings.Contains(body, "new rerun summary") {
		t.Fatal(body, err)
	}
	other, err := d.CreateTask("other", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	if err := d.Exploration(other.ExplorationID).MainDispatchFeedback(t.Context(), id, 0, true); err == nil {
		t.Fatal("cross-task subscription accepted")
	}
}

func TestWorkerFeedbackConcurrentDedupAndRollback(t *testing.T) {
	d, s, task := executionFixture(t)
	id := addQueueIntent(t, s, "concurrent", 5)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.MainDispatchFeedback(t.Context(), id, 1, true) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	tx, err := s.beginQueue()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE exploration_nodes SET state='stopped' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	var count int
	d.QueryRow(`SELECT count(*) FROM worker_feedback_delivery WHERE task_id=$1`, task).Scan(&count)
	if count != 0 {
		t.Fatal("rollback leaked feedback", count)
	}
	if err = s.SetIntentState(id, "stopped"); err != nil {
		t.Fatal(err)
	}
	d.QueryRow(`SELECT count(*) FROM worker_feedback_delivery WHERE task_id=$1`, task).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	// Deleting the owning task must not be prevented by feedback triggers/FKs.
	if err = d.DeleteTask(task); err != nil {
		t.Fatal(err)
	}
}
