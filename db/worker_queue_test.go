package db

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func queueFixture(t *testing.T) (*DB, *ExplorationStore) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	exp, err := d.CreateExploration("queue", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM explorations WHERE id=$1`, exp); d.Close() })
	return d, d.Exploration(exp)
}
func addQueueIntent(t *testing.T, s *ExplorationStore, title string, priority int) int64 {
	t.Helper()
	id, err := s.AddIntent(map[string]any{"summary": title}, priority, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestWorkerQueueOrderPersistenceAndAppend(t *testing.T) {
	d, s := queueFixture(t)
	a := addQueueIntent(t, s, "a", 1)
	b := addQueueIntent(t, s, "b", 10)
	c := addQueueIntent(t, s, "c", 5)
	q, err := s.WorkerQueue()
	if err != nil || q.Items[0].ID != b {
		t.Fatalf("default queue %+v %v", q, err)
	}
	moved, err := s.MoveWorker(a, &b, q.Version)
	if err != nil || !moved.Manual || moved.Items[0].ID != a {
		t.Fatalf("move %+v %v", moved, err)
	}
	if _, err = s.MoveWorker(c, nil, q.Version); !errors.Is(err, ErrIntentStateConflict) {
		t.Fatal("stale version accepted", err)
	}
	newer := addQueueIntent(t, s, "new priority 10", 10)
	q, err = d.Exploration(s.ID()).WorkerQueue()
	if err != nil || q.Items[0].ID != a || q.Items[3].ID != newer {
		t.Fatalf("persistent order %+v %v", q, err)
	}
	if _, err = s.StopIntentWithReason(b, "user", "user"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ReopenIntentByUser(b, "stopped"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	q, _ = s.WorkerQueue()
	if q.Items[len(q.Items)-1].ID != b {
		t.Fatal("resume did not append")
	}
	for _, id := range []int64{a, c, newer, b} {
		n, err := s.ClaimNextWorker("test", nil)
		if err != nil || n == nil || n.ID != id {
			t.Fatalf("claim want %d got %+v %v", id, n, err)
		}
	}
}
func TestDeleteWorkerPreservesProductsAndMetering(t *testing.T) {
	d, s := queueFixture(t)
	id := addQueueIntent(t, s, "remove worker", 1)
	fact, err := s.AddNode("fact", map[string]any{"summary": "keep fact"}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Link(id, "yields", fact); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendActivity(Activity{NodeID: &id, Worker: "worker", Kind: "result", Summary: "work", InputTokens: newInt(31), OutputTokens: newInt(7)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StopIntentWithReason(id, "not needed", "user"); err != nil {
		t.Fatal(err)
	}
	finding, err := s.AddNode("finding", map[string]any{"summary": "keep finding", "severity": "high"}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Link(id, RelYields, finding); err != nil {
		t.Fatal(err)
	}
	fid, err := s.AddStandaloneFinding(0, finding, "test", "keep finding", SeverityHigh, "summary", "evidence", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.DeleteWorker(id)
	if !changed || err != nil {
		t.Fatal(changed, err)
	}
	changed, err = s.DeleteWorker(id)
	if changed || err != nil {
		t.Fatal("not idempotent", changed, err)
	}
	n, _ := s.GetNode(id)
	if n != nil {
		t.Fatal("worker remains")
	}
	n, _ = s.GetNode(fact)
	if n == nil {
		t.Fatal("product deleted")
	}
	var findingExists bool
	if err = d.QueryRow(`SELECT EXISTS(SELECT 1 FROM findings WHERE id=$1)`, fid).Scan(&findingExists); err != nil || !findingExists {
		t.Fatal("standalone finding lost", err)
	}
	var count, total int
	if err = d.QueryRow(`SELECT count(*),coalesce(sum(input_tokens),0) FROM activity WHERE exploration_id=$1 AND worker='token-ledger'`, s.ID()).Scan(&count, &total); err != nil || count != 1 || total != 31 {
		t.Fatal(count, total, err)
	}
	receipts, err := s.DeletedWorkers()
	if err != nil || len(receipts) != 1 || !receipts[0].UserCancelled() {
		t.Fatal("lost cancellation receipt", err)
	}
	if ok, err := s.ClaimIntent(id, "worker"); ok || err != nil {
		t.Fatal("deleted claimed", ok, err)
	}
	other := addQueueIntent(t, s, "running", 1)
	if _, err = s.ClaimIntent(other, "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteWorker(other); !errors.Is(err, ErrIntentStateConflict) {
		t.Fatal("running deleted", err)
	}
}
func TestWorkerDeleteClaimRace(t *testing.T) {
	_, s := queueFixture(t)
	for i := 0; i < 12; i++ {
		id := addQueueIntent(t, s, fmt.Sprintf("race %d", i), 1)
		var wg sync.WaitGroup
		wg.Add(2)
		var deleted, claimed bool
		var de, ce error
		go func() { defer wg.Done(); deleted, de = s.DeleteWorker(id) }()
		go func() { defer wg.Done(); claimed, ce = s.ClaimIntent(id, "worker") }()
		wg.Wait()
		if ce != nil || de != nil && !errors.Is(de, ErrIntentStateConflict) {
			t.Fatal(de, ce)
		}
		if deleted == claimed {
			t.Fatalf("delete=%v claim=%v", deleted, claimed)
		}
	}
}

func newInt(n int) *int { return &n }

func TestWorkerReorderClaimRace(t *testing.T) {
	_, s := queueFixture(t)
	for i := 0; i < 12; i++ {
		a := addQueueIntent(t, s, fmt.Sprintf("first-%d", i), 10)
		b := addQueueIntent(t, s, fmt.Sprintf("last-%d", i), 1)
		q, err := s.WorkerQueue()
		if err != nil {
			t.Fatal(err)
		}
		var moveErr, claimErr error
		var claimed *Node
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, moveErr = s.MoveWorker(b, &a, q.Version) }()
		go func() { defer wg.Done(); claimed, claimErr = s.ClaimNextWorker("race", nil) }()
		wg.Wait()
		if claimErr != nil || claimed == nil {
			t.Fatal("claim failed", claimErr)
		}
		if moveErr == nil && claimed.ID != b {
			t.Fatal("claim used stale order")
		}
		if moveErr != nil && (!errors.Is(moveErr, ErrIntentStateConflict) || claimed.ID != a) {
			t.Fatal("unexpected conflict", moveErr, claimed.ID)
		}
		if _, err = s.ClaimNextWorker("drain", nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerDeleteCleanupFailureRollsBack(t *testing.T) {
	_, s := queueFixture(t)
	id := addQueueIntent(t, s, "cleanup rollback", 1)
	failure := errors.New("file stage failed")
	if changed, err := s.DeleteWorkerWithCleanup(id, func() error { return failure }); changed || !errors.Is(err, failure) {
		t.Fatal(changed, err)
	}
	n, err := s.GetNode(id)
	if err != nil || n == nil || n.State != "open" {
		t.Fatal("worker lost after failed cleanup", err)
	}
	deleted, err := s.DeletedWorkers()
	if err != nil || len(deleted) != 0 {
		t.Fatal("receipt committed despite rollback", err)
	}
	q, err := s.WorkerQueue()
	if err != nil || len(q.Items) != 1 {
		t.Fatal("queue lost worker", err)
	}
}

func TestWorkerQueueArchiveRowsPreserveOrderAndDeletion(t *testing.T) {
	d, s := queueFixture(t)
	a := addQueueIntent(t, s, "archive a", 10)
	b := addQueueIntent(t, s, "archive b", 1)
	gone := addQueueIntent(t, s, "archive deleted", 5)
	if _, err := s.DeleteWorker(gone); err != nil {
		t.Fatal(err)
	}
	q, _ := s.WorkerQueue()
	if _, err := s.MoveWorker(b, &a, q.Version); err != nil {
		t.Fatal(err)
	}
	var root, nodes, deleted []byte
	for _, item := range []struct {
		query string
		dest  *[]byte
	}{
		{`SELECT json_agg(e) FROM explorations e WHERE id=$1`, &root},
		{`SELECT json_agg(n ORDER BY id) FROM exploration_nodes n WHERE exploration_id=$1`, &nodes},
		{`SELECT json_agg(d) FROM deleted_workers d WHERE exploration_id=$1`, &deleted},
	} {
		if err := d.QueryRow(item.query, s.ID()).Scan(item.dest); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := s.beginQueue()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM exploration_nodes WHERE exploration_id=$1`, s.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM deleted_workers WHERE exploration_id=$1`, s.ID()); err != nil {
		t.Fatal(err)
	}
	if err = restoreExplorationStub(tx, root, s.ID()); err != nil {
		t.Fatal(err)
	}
	if err = insertArchiveRows(tx, "exploration_nodes", nodes); err != nil {
		t.Fatal(err)
	}
	if err = insertArchiveRows(tx, "deleted_workers", deleted); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	q, err = s.WorkerQueue()
	if err != nil || !q.Manual || len(q.Items) != 2 || q.Items[0].ID != b {
		t.Fatalf("archive order %+v %v", q, err)
	}
	receipts, err := s.DeletedWorkers()
	if err != nil || len(receipts) != 1 || receipts[0].ID != gone {
		t.Fatal("archive lost deletion receipt", err)
	}
}
