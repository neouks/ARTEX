package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestExpandIndexUsesOverviewRepresentative(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("digest index", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as := d.Assets()
	var assets []int64
	for i := range 2 {
		id, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: fmt.Sprintf("index-%d-%d.test", task.ID, i), TaskID: task.ID})
		if err != nil {
			t.Fatal(err)
		}
		assets = append(assets, id)
	}
	store := d.Exploration(task.ExplorationID)
	member, err := store.AddNode(db.KindFact, map[string]any{"summary": "shared", "asset_ids": assets}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range assets {
		if err := store.Anchor(member, id); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := store.AddDigest(map[string]any{"body": "shared digest"}, []int64{member})
	if err != nil {
		t.Fatal(err)
	}
	tools := &ToolSet{ts: store, as: as, taskID: task.ID}
	_, index := tools.coldDigestOverview()
	if len(index) != 1 || index[0]["asset_id"] != assets[0] {
		t.Fatalf("unexpected representative: %+v", index)
	}
	for _, id := range []int64{assets[0], assets[1], 0} {
		result, err := tools.expandIndex().Call(t.Context(), json.RawMessage(fmt.Sprintf(`{"asset_id":%d}`, id)), nil)
		if err != nil || result.IsError {
			t.Fatalf("expand: %+v %v", result, err)
		}
		var out struct {
			Digests []struct{ ID int64 } `json:"digests"`
		}
		if err := json.Unmarshal([]byte(result.Flatten()), &out); err != nil {
			t.Fatal(err)
		}
		want := 0
		if id == assets[0] {
			want = 1
		}
		if len(out.Digests) != want || (want == 1 && out.Digests[0].ID != digest) {
			t.Fatalf("asset %d: %+v", id, out)
		}
	}
}

func TestDigestAuthorizationAcrossTasks(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	source, err := d.CreateTask("digest source", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(source.ID)
	current, err := d.CreateTaskWithOptions("digest current", "goal", db.TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(current.ID)
	as := d.Assets()
	root, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: fmt.Sprintf("digest-%d.test", source.ID), TaskID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(source.ExplorationID)
	member, err := store.AddNode(db.KindFact, map[string]any{"summary": "secret digest fact", "asset_ids": []int64{root}}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.AddDigest(map[string]any{"body": "secret digest body"}, []int64{member})
	if err != nil {
		t.Fatal(err)
	}
	local := &ToolSet{ts: store, as: as, taskID: source.ID}
	inherited := &ToolSet{ts: d.Exploration(current.ExplorationID), as: as, taskID: current.ID}
	check := func(tools *ToolSet, allowed bool) {
		t.Helper()
		if got := tools.digestAuthorized(store, source.ID, digest); got != allowed {
			t.Fatalf("authorized=%v want %v", got, allowed)
		}
		if got := len(tools.activeDigestBodies(store, source.ID)) > 0; got != allowed {
			t.Fatalf("digest bodies visible=%v", got)
		}
		if got := len(tools.authorizedCoveredMembers(store, source.ID)) > 0; got != allowed {
			t.Fatalf("covered IDs visible=%v", got)
		}
		for _, tool := range []string{"expand", "detail"} {
			read := tools.expandDigest()
			if tool == "detail" {
				read = tools.nodeDetail()
			}
			res, err := read.Call(t.Context(), json.RawMessage(fmt.Sprintf(`{"id":%d}`, digest)), nil)
			if err != nil || res.IsError == allowed {
				t.Fatalf("%s allowed=%v result=%+v err=%v", tool, allowed, res, err)
			}
		}
	}
	check(local, true)
	check(inherited, true)
	if _, err := as.DetachAssetFromTask(current.ID, root); err != nil {
		t.Fatal(err)
	}
	check(inherited, false)
	check(local, true)
	if err := as.BlockTaskAssets(source.ID, []int64{root}, "user", "test"); err != nil {
		t.Fatal(err)
	}
	check(local, false)
	if cds, idx := local.coldDigestOverview(); len(cds) != 0 || len(idx) != 0 {
		t.Fatal("blocked digest leaked through index")
	}
	n, err := store.GetNode(member)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCompactor(nil, "")
	c.SetAssetStore(as)
	if c.blockAuthorized(store, block{Members: []int64{member}}, map[int64]*db.Node{member: n}) {
		t.Fatal("blocked member can reach compactor")
	}
	if err := as.ApproveTaskAssets(source.ID, []int64{root}, "user", "test"); err != nil {
		t.Fatal(err)
	}
	check(local, true)
	check(inherited, false)
}
