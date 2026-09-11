package db

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTaskAssetManagementView(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("view", "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	s := d.Assets()
	ids := map[string]int64{}
	for _, name := range []string{"approved", "pending", "revoked", "blocked", "deleted"} {
		id, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: name + ".test", TaskID: task.ID, AgentDiscovered: true})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	if err := s.ApproveTaskAssets(task.ID, []int64{ids["approved"], ids["revoked"]}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeTaskAssets(task.ID, []int64{ids["revoked"]}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.BlockTaskAssets(task.ID, []int64{ids["blocked"]}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DetachAssetFromTask(task.ID, ids["deleted"]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertSubdomain(UpsertSubdomainReq{Domain: "approved.test", RecordType: "TXT", RecordValue: []string{"data"}, TaskID: task.ID, AgentDiscovered: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertEndpoint(UpsertEndpointReq{URL: "https://approved.test/path", Method: "GET", TaskID: task.ID, AgentDiscovered: true}); err != nil {
		t.Fatal(err)
	}
	out, err := s.QueryTaskAssetView(task.ID, TaskAssetViewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || len(out.Assets) != 1 || out.Assets[0].RecordCount != 2 || !out.Assets[0].CanSchedule {
		t.Fatalf("default/grouping: %+v", out)
	}
	for state, want := range map[string]int{"approved": 1, "pending": 1, "blocked": 2, "revoked": 1} {
		if out.Counts[state] != want {
			t.Fatalf("counts: %+v", out.Counts)
		}
	}
	var cursor string
	seen := map[string]bool{}
	for {
		page, err := s.QueryTaskAssetView(task.ID, TaskAssetViewQuery{Status: "all", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Assets {
			if seen[row.GroupKey] {
				t.Fatal("duplicate page")
			}
			seen[row.GroupKey] = true
			if row.Host == "deleted.test" && (row.AssetID != nil || row.CanSchedule) {
				t.Fatalf("tombstone: %+v", row)
			}
		}
		if cursor == "" && page.NextCursor != "" {
			for _, bad := range []TaskAssetViewQuery{{Status: "pending", Cursor: page.NextCursor}, {Status: "all", Q: "different", Cursor: page.NextCursor}} {
				if _, err := s.QueryTaskAssetView(task.ID, bad); err == nil {
					t.Fatal("cursor binding bypassed")
				}
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("pages: %v", seen)
	}
	summary, err := s.QueryTaskAssetView(task.ID, TaskAssetViewQuery{Status: "all", SummaryOnly: true})
	if err != nil || len(summary.Assets) != 0 || summary.NextCursor != "" {
		t.Fatalf("summary: %+v %v", summary, err)
	}
	small, _ := json.Marshal(summary)
	full, _ := json.Marshal(out)
	if len(small) >= len(full) {
		t.Fatal("summary did not reduce response")
	}
	t.Logf("summary=%d bytes page=%d bytes", len(small), len(full))
	for _, q := range []TaskAssetViewQuery{{Status: "bad"}, {Limit: 51}, {Limit: -1}, {Cursor: "bad"}} {
		if _, err := s.QueryTaskAssetView(task.ID, q); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	search, err := s.QueryTaskAssetView(task.ID, TaskAssetViewQuery{Status: "all", Q: "PENDING"})
	if err != nil || search.Total != 1 || search.Counts["approved"] != 0 {
		t.Fatalf("search: %+v %v", search, err)
	}
	child, err := d.CreateTaskWithOptions("inherited", "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets", SourceTaskIDs: []int64{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(child.ID)
	inherited, err := s.QueryTaskAssetView(child.ID, TaskAssetViewQuery{})
	if err != nil || len(inherited.Assets) != 1 || !inherited.Assets[0].ReadOnly || inherited.Assets[0].OwnerTaskID != task.ID {
		t.Fatalf("inherited: %+v %v", inherited, err)
	}
	if _, err := s.QueryTaskAssetView(task.ID+100000, TaskAssetViewQuery{}); err == nil {
		t.Fatal("missing task returned zero counts")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.WithReadContext(cancelled).QueryTaskAssetView(task.ID, TaskAssetViewQuery{SummaryOnly: true}); err == nil {
		t.Fatal("query failure returned fake counts")
	}
	if empty, err := s.QueryTaskAssetView(task.ID, TaskAssetViewQuery{Status: "all", Q: "%"}); err != nil || empty.Total != 0 || len(empty.Assets) != 0 {
		t.Fatalf("literal search/empty: %+v %v", empty, err)
	}
	if _, err := s.AttachAssetsToTask(child.ID, []int64{ids["approved"]}, "用户关联"); err != nil {
		t.Fatal(err)
	}
	current, err := s.QueryTaskAssetView(child.ID, TaskAssetViewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	override := false
	for _, row := range current.Assets {
		if row.OwnerTaskID == child.ID && row.AssetID != nil && *row.AssetID == ids["approved"] {
			override = !row.ReadOnly && row.CanSchedule
		}
	}
	if !override {
		t.Fatalf("current association did not override source: %+v", current)
	}
}
