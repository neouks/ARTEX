package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestUserCancellationLifecycle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("test", "cancel lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	s := d.Exploration(exp)
	id, err := s.AddIntent(map[string]any{"summary": "cancel me"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := s.StopIntentWithReason(id, "", "user")
	if err != nil || fact == 0 {
		t.Fatalf("cancel: %d %v", fact, err)
	}
	before, _ := s.GetNode(id)
	if !before.UserCancelled() || before.State != "stopped" {
		t.Fatalf("not cancelled: %+v", before)
	}
	var completed bool
	if err := d.QueryRow(`SELECT completed_at IS NOT NULL AND content_version>0 FROM exploration_nodes WHERE id=$1`, id).Scan(&completed); err != nil || !completed {
		t.Fatalf("completion/version: %v", err)
	}
	again, err := s.StopIntentWithReason(id, "different reason", "user")
	if err != nil || again != 0 {
		t.Fatalf("duplicate: %d %v", again, err)
	}
	for _, state := range []string{"open", "running", "blocked", "paused"} {
		if err := s.SetIntentState(id, state); err != nil {
			t.Fatal(err)
		}
		if err := s.SetNodeState(id, state); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.CompareAndSetIntentState(id, "stopped", state); err != nil || ok {
			t.Fatalf("CAS reopened: %v %v", ok, err)
		}
	}
	if ok, err := s.ReopenIntent(id); err != nil || ok {
		t.Fatalf("automatic reopen: %v %v", ok, err)
	}
	if ok, err := s.ClaimIntent(id, "worker"); err != nil || ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	for _, state := range []string{"running", "blocked", "open"} {
		// Model stale/legacy state; the persisted cancellation marker must still win.
		if _, err := d.Exec(`UPDATE exploration_nodes SET state=$2 WHERE id=$1`, id, state); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResetRunningIntents(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReopenBlockedIntents(); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.ClaimIntent(id, "worker"); err != nil || ok {
			t.Fatalf("legacy claim: %v %v", ok, err)
		}
		fr, err := s.Frontier(100)
		if err != nil || len(fr) != 0 {
			t.Fatalf("frontier: %v %v", fr, err)
		}
	}
	_, _ = d.Exec(`UPDATE exploration_nodes SET state='stopped' WHERE id=$1`, id)
	if ok, err := s.ReopenIntentByUser(id, "stopped"); err != nil || !ok {
		t.Fatalf("user reopen: %v %v", ok, err)
	}
	if err := s.RestoreUserReopen(before); err != nil {
		t.Fatal(err)
	}
	restored, _ := s.GetNode(id)
	if !restored.UserCancelled() {
		t.Fatal("rollback lost lock")
	}
	if ok, err := s.ReopenIntentByUser(id, "stopped"); err != nil || !ok {
		t.Fatalf("reopen: %v %v", ok, err)
	}
	reopened, _ := s.GetNode(id)
	if reopened.UserCancelled() {
		t.Fatal("lock retained")
	}
	var p map[string]any
	_ = json.Unmarshal(reopened.Payload, &p)
	if p["cancel_reason"] != "用户取消等待运行" || p["reopened_by_user_at"] == nil {
		t.Fatal("audit lost", p)
	}
	if nodes, err := s.UserCancelledIntents(); err != nil || len(nodes) != 0 {
		t.Fatalf("active list: %v %v", nodes, err)
	}
	if ok, err := s.ClaimIntent(id, "worker"); err != nil || !ok {
		t.Fatalf("reopened claim: %v %v", ok, err)
	}
	if n, _ := s.GetNode(fact); n == nil {
		t.Fatal("audit fact removed")
	}
}

func TestUserCancellationClaimRace(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("test", "cancel race")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	s := d.Exploration(exp)
	for i := 0; i < 20; i++ {
		id, err := s.AddIntent(map[string]any{"summary": fmt.Sprint("race", i)}, 1, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		claimed := make(chan bool, 1)
		claimErr := make(chan error, 1)
		go func() { ok, err := s.ClaimIntent(id, "worker"); claimed <- ok; claimErr <- err }()
		_, stopErr := s.StopIntentWithReason(id, "cancel", "user")
		ok := <-claimed
		if err := <-claimErr; err != nil {
			t.Fatal(err)
		}
		if ok {
			if !errors.Is(stopErr, ErrIntentStateConflict) {
				t.Fatalf("running work cancelled without stop: %v", stopErr)
			}
		} else if stopErr != nil {
			t.Fatal(stopErr)
		}
	}
}
