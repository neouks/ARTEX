package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestToolNodePageAuthorizationBeforePaging(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("tool paging", "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	es := d.Exploration(task.ExplorationID)
	approved, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "tool-approved.test", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: "tool-pending.test", TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	var visible []int64
	for i := 0; i < 3; i++ {
		id, err := es.AddNode(KindFact, map[string]any{"summary": "page-regression visible"}, 0, "confirmed", "test", []int64{approved})
		if err != nil {
			t.Fatal(err)
		}
		visible = append(visible, id)
	}
	for i := 0; i < 30; i++ {
		if _, err := es.AddNode(KindFact, map[string]any{"summary": "page-regression hidden"}, 0, "confirmed", "test", []int64{pending}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := es.ToolNodePage(context.Background(), KindFact, "page-regression", "", 0, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Nodes) != 3 || page.Nodes[0].ID != visible[2] {
		t.Fatalf("authorized page: %+v", page)
	}
	next, err := es.ToolNodePage(context.Background(), KindFact, "page-regression", "", 0, page.Nodes[1].ID, 2)
	if err != nil || next.Total != 3 || len(next.Nodes) != 1 || next.Nodes[0].ID != visible[0] {
		t.Fatalf("continuation: %+v %v", next, err)
	}
	if err := d.Assets().RevokeTaskAssets(task.ID, []int64{approved}, "user", ""); err != nil {
		t.Fatal(err)
	}
	page, err = es.ToolNodePage(context.Background(), KindFact, "page-regression", "", 0, 0, 2)
	if err != nil || page.Total != 0 || len(page.Nodes) != 0 {
		t.Fatalf("revoke: %+v %v", page, err)
	}
}

func TestToolReadProjectionSizes(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, size := range []int{10, 100, 1000} {
		exp, err := d.CreateExploration("projection size", "goal")
		if err != nil {
			t.Fatal(err)
		}
		defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
		if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,state,payload)
  SELECT $1,'fact','confirmed',jsonb_build_object('summary','fixture '||n,'detail',repeat('大段证据',2048)) FROM generate_series(1,$2::int)n`, exp, size); err != nil {
			t.Fatal(err)
		}
		es := d.Exploration(exp)
		started := time.Now()
		raw, _, _, err := es.ListByKindPageWithSources(KindFact, 0, 20, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		fullTime := time.Since(started)
		started = time.Now()
		page, err := es.ToolNodePage(context.Background(), KindFact, "fixture", "", 0, 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		projectTime := time.Since(started)
		oldBytes, _ := json.Marshal(raw)
		newBytes, _ := json.Marshal(page)
		if len(newBytes) >= len(oldBytes) {
			t.Fatal("projection did not shrink evidence-heavy page")
		}
		t.Logf("rows=%d full_page=%dB/%s projected_page=%dB/%s (database projection, not exact model Tokens)", size, len(oldBytes), fullTime, len(newBytes), projectTime)
	}
}

func TestLatestWorkerOutputBeyondThousandActivities(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("output", "goal")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	es := d.Exploration(exp)
	id, err := es.AddIntent(map[string]any{"summary": "run"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := es.LatestWorkerOutput(context.Background(), id); err != nil || out != nil {
		t.Fatalf("empty: %+v %v", out, err)
	}
	if _, err := d.Exec(`INSERT INTO activity(exploration_id,node_id,kind,detail) SELECT $1,$2,'text','phase-'||n FROM generate_series(1,1100)n`, exp, id); err != nil {
		t.Fatal(err)
	}
	out, err := es.LatestWorkerOutput(context.Background(), id)
	if err != nil || out == nil || out.Detail != "phase-1100" {
		t.Fatalf("latest text: %+v %v", out, err)
	}
	if _, err := d.Exec(`INSERT INTO activity(exploration_id,node_id,kind,detail) VALUES($1,$2,'result','最终结论'),($1,$2,'text','later text')`, exp, id); err != nil {
		t.Fatal(err)
	}
	out, err = es.LatestWorkerOutput(context.Background(), id)
	if err != nil || out == nil || out.Detail != "最终结论" {
		t.Fatalf("latest result: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := es.LatestWorkerOutput(ctx, id); err == nil {
		t.Fatal("query failure disguised as no output")
	}
}
