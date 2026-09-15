package db

import (
	"reflect"
	"testing"
)

func TestTargetAccessLineageAndSources(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	source, err := d.CreateTaskWithOptions("access source", "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(source.ID)
	current, err := d.CreateTaskWithOptions("access current", "goal", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}, AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(current.ID)
	other, err := d.CreateTask("unrelated", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	as := d.Assets()
	store := d.Exploration(source.ExplorationID)
	asset := func(host string, taskID int64) int64 {
		id, e := as.UpsertSubdomain(UpsertSubdomainReq{Domain: host, TaskID: taskID, AgentDiscovered: true})
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	a := asset("first.lineage-access.test", source.ID)
	b := asset("second.lineage-access.test", source.ID)
	unrelated := asset("other.lineage-access.test", other.ID)
	if err := as.ApproveTaskAssets(source.ID, []int64{a}, "user", ""); err != nil {
		t.Fatal(err)
	}
	node := func(kind string, payload map[string]any, ids []int64) int64 {
		state := "confirmed"
		if kind == KindDigest {
			state = "active"
		}
		id, e := store.AddNode(kind, payload, 0, state, "test", ids)
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	parent := node(KindFact, map[string]any{"summary": "parent"}, []int64{a, b})
	child := node(KindFact, map[string]any{"summary": "child"}, nil)
	if err := store.Link(parent, RelDerivedFrom, child); err != nil {
		t.Fatal(err)
	}
	digest := node(KindDigest, map[string]any{"summary": "digest", "anchor_ids": []int64{child}}, nil)
	if err := store.Link(digest, "covers", child); err != nil {
		t.Fatal(err)
	}
	empty := node(KindDigest, map[string]any{"summary": "empty"}, nil)
	broken := node(KindDigest, map[string]any{"summary": "broken", "anchor_ids": []int64{99999999}}, nil)
	check := func(want []string) {
		t.Helper()
		states, e := as.TaskNodeAccess(current.ID, []int64{parent, child, digest, broken, empty, 99999999})
		if e != nil {
			t.Fatal(e)
		}
		for _, id := range []int64{parent, child, digest} {
			if states[id].CanRead != (len(want) == 0) || !reflect.DeepEqual(states[id].Reasons, want) {
				t.Fatalf("node %d=%+v want %v", id, states[id], want)
			}
		}
		for _, id := range []int64{broken, empty, 99999999} {
			if states[id].CanRead || !reflect.DeepEqual(states[id].Reasons, []string{"unavailable"}) {
				t.Fatalf("unknown %d=%+v", id, states[id])
			}
		}
	}
	check([]string{"pending"})
	worker, e := as.WithWorkerRead().TaskNodeAccess(current.ID, []int64{child, digest})
	if e != nil || !worker[child].CanRead || !worker[digest].CanRead {
		t.Fatalf("worker pending regression %+v %v", worker, e)
	}
	if err := as.ApproveTaskAssets(source.ID, []int64{b}, "user", ""); err != nil {
		t.Fatal(err)
	}
	check([]string{})
	// A local approval must not override the source node's revoked asset.
	if id := asset("first.lineage-access.test", current.ID); id != a {
		t.Fatal("identity changed")
	}
	if err := as.ApproveTaskAssets(current.ID, []int64{a}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := as.RevokeTaskAssets(source.ID, []int64{a}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := as.BlockTaskAssets(source.ID, []int64{b}, "user", ""); err != nil {
		t.Fatal(err)
	}
	check([]string{"blocked", "revoked"})
	states, e := as.TaskTargetAssetStates(current.ID, []int64{a, b, unrelated, 99999999})
	if e != nil {
		t.Fatal(e)
	}
	if states[a] != "approved" || states[b] != "blocked" || states[unrelated] != "unavailable" || states[99999999] != "unavailable" {
		t.Fatalf("assets %+v", states)
	}
	if _, err := as.DeleteByIDs([]int64{a}); err != nil {
		t.Fatal(err)
	}
	states, e = as.TaskTargetAssetStates(current.ID, []int64{a})
	if e != nil || states[a] != "unavailable" {
		t.Fatalf("deleted asset %+v %v", states, e)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := as.TaskNodeAccess(current.ID, []int64{child}); err == nil {
		t.Fatal("query error hidden")
	}
	if _, err := as.TaskTargetAssetStates(current.ID, []int64{a}); err == nil {
		t.Fatal("asset query error hidden")
	}
}
