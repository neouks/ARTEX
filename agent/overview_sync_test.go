package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// Fixed fixture for comparing overview input size and SQL calls across syncs.
func TestOverviewSyncFixture(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("overview sync fixture", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	store := d.Exploration(task.ExplorationID)
	for _, spec := range []struct {
		kind, state string
		count       int
	}{{db.KindFact, "confirmed", 120}, {db.KindFinding, "confirmed", 35}, {db.KindIntent, "done", 80}, {db.KindIntent, "open", 60}} {
		if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,state,priority,payload) SELECT $1,$2,$3,i%5,jsonb_build_object('summary',repeat('固定样例 evidence ',20)) FROM generate_series(1,$4::int) i`, task.ExplorationID, spec.kind, spec.state, spec.count); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 24; i++ {
		member, err := store.AddNode(db.KindFact, map[string]any{"summary": "digest member"}, 0, "confirmed", "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.AddDigest(map[string]any{"body": "fixed digest body"}, []int64{member}); err != nil {
			t.Fatal(err)
		}
	}
	tools := NewToolSet(store, "planner")
	tools.SetTaskID(task.ID)
	tools.SetAssetStore(d.Assets(), d.Companies())
	measure := os.Getenv("ARTEX_MEASURE_OVERVIEW") == "1"
	if measure {
		if _, err = d.Exec(`CREATE EXTENSION IF NOT EXISTS pg_stat_statements`); err != nil {
			t.Fatal(err)
		}
		if _, err = d.Exec(`SELECT pg_stat_statements_reset()`); err != nil {
			t.Fatal(err)
		}
	}
	out := tools.graphOverviewData()
	if out["partial"] == true {
		t.Fatalf("partial overview: %v", out["unavailable"])
	}
	for key, want := range map[string]int{"open_intents": 30, "finding_list": 10, "recent_facts": 20, "recent_done_intents": 12, "cold_digests": 15} {
		rows, ok := out[key].([]map[string]any)
		if !ok || len(rows) != want {
			t.Errorf("%s length=%d want=%d", key, len(rows), want)
		}
	}
	if out["frontier_open"] != 60 || out["findings_total"] != 35 {
		t.Fatalf("incorrect totals: frontier=%v findings=%v", out["frontier_open"], out["findings_total"])
	}
	if more, ok := out["cold_digests_more"].([]int64); !ok || len(more) != 9 {
		t.Fatalf("overflow=%v", out["cold_digests_more"])
	}
	for _, key := range []string{"cold_index", "covered_members", "findings", "unfolded_truncated"} {
		if _, ok := out[key]; ok {
			t.Errorf("obsolete field %s", key)
		}
	}
	for _, row := range out["finding_list"].([]map[string]any) {
		if len(row) > 3 || row["assets"] != nil || row["evidence"] != nil {
			t.Errorf("bloated finding: %v", row)
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if measure {
		var calls int
		if err = d.QueryRow(`SELECT COALESCE(sum(calls),0)::int FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) AND query NOT LIKE '%pg_stat_statements%'`).Scan(&calls); err != nil {
			t.Fatal(err)
		}
		t.Logf("overview fixture: bytes=%d sql_calls=%d", len(raw), calls)
	}
}

func TestOverviewDigestRecentOrderingAndErrors(t *testing.T) {
	d := testDB(t)
	task, err := d.CreateTask("recent digests", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(task.ExplorationID)
	var members []int64
	for i := 0; i < 20; i++ {
		id, err := store.AddNode(db.KindFact, map[string]any{"summary": "member"}, 0, "confirmed", "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, id)
	}
	// Reverse digest creation order: freshness must come from members, not digests.
	var ids []int64
	for i := len(members) - 1; i >= 0; i-- {
		id, err := store.AddDigest(map[string]any{"body": "body"}, []int64{members[i]})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	tools := NewToolSet(store, "planner")
	tools.SetTaskID(task.ID)
	tools.SetAssetStore(d.Assets(), d.Companies())
	rows, more, err := tools.coldDigestsRecent(store, task.ID, 15)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 15 || len(more) != 5 {
		t.Fatalf("lengths=%d/%d", len(rows), len(more))
	}
	for i, row := range rows {
		if row["id"] != ids[i] {
			t.Fatalf("order at %d: %v != %d", i, row["id"], ids[i])
		}
	}
	for i, id := range more {
		if id != ids[15+i] {
			t.Fatalf("overflow order=%v", more)
		}
	}
	// Source summaries keep their own smaller cap and inherited origin.
	child, err := d.CreateTaskWithOptions("child digests", "goal", db.TaskCreateOptions{SourceTaskIDs: []int64{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	inherited := NewToolSet(d.Exploration(child.ExplorationID), "planner")
	inherited.SetTaskID(child.ID)
	inherited.SetAssetStore(d.Assets(), d.Companies())
	related, err := inherited.relatedTaskOverviews()
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != 1 {
		t.Fatalf("related=%v", related)
	}
	sourceRows := related[0]["cold_digests"].([]map[string]any)
	if len(sourceRows) != 6 || len(related[0]["cold_digests_more"].([]int64)) != 14 {
		t.Fatalf("related limits=%v", related[0])
	}
	for _, row := range sourceRows {
		if row["source_task_id"] != task.ID || row["inherited"] != true {
			t.Fatalf("origin=%v", row)
		}
	}
	if err = d.DeleteTask(child.ID); err != nil {
		t.Fatal(err)
	}
	if err = d.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, _, err = tools.coldDigestsRecent(store, task.ID, 15); err == nil {
		t.Fatal("query failure swallowed")
	}
	out := tools.graphOverviewData()
	if out["partial"] != true || out["findings_total"] != nil || out["frontier_open"] != nil {
		t.Fatalf("database failure reported as empty: %v", out)
	}
}
