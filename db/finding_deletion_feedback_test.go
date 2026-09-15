package db

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestFindingDeletionFeedbackTransaction(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("delete feedback", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	es := d.Exploration(task.ExplorationID)
	create := func() (int64, int64) {
		n, e := es.AddNode(KindFinding, map[string]any{"summary": "test"}, 0, "confirmed", "worker", nil)
		if e != nil {
			t.Fatal(e)
		}
		id, e := d.AddFinding(task.ID, n, "XSS", "title", "high", "summary", "secret-evidence", "worker", nil)
		if e != nil {
			t.Fatal(e)
		}
		return id, n
	}
	id, node := create()
	feedback, err := d.DeleteFindingWithFeedback(id, "  证据不足  ")
	if err != nil || feedback == nil || feedback.Reason != "证据不足" {
		t.Fatalf("feedback %+v %v", feedback, err)
	}
	if f, _ := d.GetFinding(id); f != nil {
		t.Fatal("finding retained")
	}
	if n, _ := es.GetNode(node); n != nil {
		t.Fatal("node retained")
	}
	rows, err := es.FindingDeletionFeedback(0, 20)
	if err != nil || len(rows) != 1 || rows[0].FindingID != id {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if f, e := d.DeleteFindingWithFeedback(id, "duplicate"); e != nil || f != nil {
		t.Fatalf("duplicate %+v %v", f, e)
	}
	id, _ = create()
	var wg sync.WaitGroup
	results := make(chan *FindingDeletionFeedback, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f, e := d.DeleteFindingWithFeedback(id, ""); results <- f; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	count := 0
	for f := range results {
		if f != nil {
			count++
		}
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if count != 1 {
		t.Fatalf("created %d feedbacks", count)
	}
	id, _ = create()
	if _, err := d.Exec(fmt.Sprintf(`CREATE FUNCTION test_reject_finding_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.id=%d THEN RAISE EXCEPTION 'test delete failed'; END IF; RETURN OLD; END $$;
 CREATE TRIGGER test_reject_finding_delete BEFORE DELETE ON findings FOR EACH ROW EXECUTE FUNCTION test_reject_finding_delete()`, id)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeleteFindingWithFeedback(id, "rollback"); err == nil {
		t.Fatal("expected rollback")
	}
	if _, err := d.Exec(`DROP TRIGGER test_reject_finding_delete ON findings; DROP FUNCTION test_reject_finding_delete()`); err != nil {
		t.Fatal(err)
	}
	if f, _ := d.GetFinding(id); f == nil {
		t.Fatal("failed delete removed finding")
	}
	var c int
	if err := d.QueryRow(`SELECT count(*) FROM finding_deletion_feedback WHERE finding_id=$1`, id).Scan(&c); err != nil || c != 0 {
		t.Fatalf("feedback not rolled back: %d %v", c, err)
	}
	if _, err := d.DeleteFinding(id); err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateTaskWithOptions("other", "goal", TaskCreateOptions{SourceTaskIDs: []int64{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	isolated, err := d.Exploration(other.ExplorationID).FindingDeletionFeedback(0, 20)
	if err != nil || len(isolated) != 0 {
		t.Fatal("feedback inherited", isolated, err)
	}
	if err := d.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	var taskID *int64
	if err := d.QueryRow(`SELECT task_id FROM finding_deletion_feedback WHERE id=$1`, feedback.ID).Scan(&taskID); err != nil || taskID != nil {
		t.Fatalf("orphan %v %v", taskID, err)
	}
}

func TestFindingDeletionReasonValidation(t *testing.T) {
	for _, reason := range []string{"", " \n ", strings.Repeat("中", 2000)} {
		if _, err := NormalizeFindingDeletionReason(reason); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NormalizeFindingDeletionReason(strings.Repeat("中", 2001)); err == nil {
		t.Fatal("accepted long reason")
	}
}
