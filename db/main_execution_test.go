package db

import (
	"fmt"
	"sync"
	"testing"
)

func TestMainPendingIntentPermissionAndClaims(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("main permission", "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as, es := d.Assets(), d.Exploration(task.ExplorationID)
	asset, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("pending-main-%d.test", task.ID), TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	add := func(summary string) int64 {
		id, e := es.AddIntent(map[string]any{"summary": summary}, 1, []int64{asset}, "planner")
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	chosen, old := add("chosen"), add("old unapproved")
	if open, e := es.HasOpenIntent(); e != nil || open {
		t.Fatal("pending intent automatically eligible", open, e)
	}
	if ok, e := es.ClaimIntent(chosen, "worker"); e != nil || ok {
		t.Fatal("unmarked claim", ok, e)
	}
	if err := es.GrantMainIntentDispatch(chosen); err != nil {
		t.Fatal(err)
	}
	node, e := d.Exploration(task.ExplorationID).GetNode(chosen)
	if e != nil || !node.AllowsPendingAssets() || !node.DispatchRequested() {
		t.Fatal("permission not persisted", node, e)
	}
	if e := as.ValidateTaskAssetsApproved(task.ID, []int64{asset}); e == nil {
		t.Fatal("asset approval changed")
	}
	if e := as.ValidateIntentAssets(task.ID, node, []int64{asset}); e != nil {
		t.Fatal(e)
	}
	if open, e := es.HasOpenIntent(); e != nil || !open {
		t.Fatal("granted not eligible", open, e)
	}
	if _, e := es.SetExecutionMode(ExecutionManual); e != nil {
		t.Fatal(e)
	}
	if n, e := es.ClaimNextWorker("worker", nil); e != nil || n != nil {
		t.Fatal("manual reset ignored", n, e)
	}
	if e := es.SetIntentDispatch(chosen, true); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	winners := make(chan int64, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, e := es.ClaimNextWorker("worker", nil)
			if e != nil {
				t.Error(e)
			}
			if n != nil {
				winners <- n.ID
			}
		}()
	}
	wg.Wait()
	close(winners)
	count := 0
	for id := range winners {
		count++
		if id != chosen {
			t.Fatal("old pending auto started", id)
		}
	}
	if count != 1 {
		t.Fatal("duplicate worker", count)
	}
	if ok, e := es.ClaimIntent(old, "worker"); e != nil || ok {
		t.Fatal("unmarked intent admitted", ok, e)
	}
	revoked := add("revoked after grant")
	if e := es.GrantMainIntentDispatch(revoked); e != nil {
		t.Fatal(e)
	}
	if e := as.RevokeTaskAssets(task.ID, []int64{asset}, "user", ""); e != nil {
		t.Fatal(e)
	}
	if ok, e := es.ClaimIntent(revoked, "worker"); e != nil || ok {
		t.Fatal("revoked claimed", ok, e)
	}
	if e := as.ValidateIntentAssets(task.ID, node, []int64{asset}); e == nil {
		t.Fatal("revoked startup allowed")
	}
	if e := es.GrantMainIntentDispatch(old); e == nil {
		t.Fatal("revoked grant issued")
	}
}
