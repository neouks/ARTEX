package db

import (
	"errors"
	"fmt"
	"testing"
)

func TestAssetOriginRegistration(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("origin test", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	es := d.Exploration(task.ExplorationID)
	as := d.Assets()
	for _, actor := range []string{"mainagent", "planner", "work#1"} {
		t.Run(actor, func(t *testing.T) {
			var node *int64
			var owner int64
			if actor == "work#1" {
				id, e := es.AddIntent(map[string]any{"summary": "origin"}, 5, nil, "planner")
				if e != nil {
					t.Fatal(e)
				}
				owner = id
				node = &owner
			}
			seg := 3
			call := fmt.Sprintf("origin-%s-%d", actor, task.ID)
			aid, e := es.AppendActivity(Activity{Kind: "tool_use", Worker: actor, NodeID: node, MainSeg: &seg, Tool: "insert_assets", ToolUseID: call, Detail: `{"assets":[]}`})
			if e != nil {
				t.Fatal(e)
			}
			host := fmt.Sprintf("%d-%d.origin.test", task.ID, aid)
			write := func(scoped *AssetStore) (int64, error) {
				return scoped.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://" + host, TaskID: task.ID, AgentDiscovered: true})
			}
			id, e := as.RegisterAgentAssetWithOrigin(task.ID, actor, owner, call, write)
			if e != nil {
				t.Fatal(e)
			}
			check := func() {
				t.Helper()
				groups, e := as.ListTaskAssetApprovalGroups(task.ID)
				if e != nil {
					t.Fatal(e)
				}
				found := false
				for _, g := range groups {
					if g.Name != host {
						continue
					}
					found = true
					if len(g.Origins) != 1 || g.Origins[0].ActivityID != aid || !g.Origins[0].Available {
						t.Fatalf("bad origin: %+v", g)
					}
					want := "plan"
					if actor == "mainagent" {
						want = "main:3"
					}
					if owner > 0 {
						want = fmt.Sprintf("intent:%d", owner)
					}
					if g.Origins[0].Session != want {
						t.Fatalf("session=%s", g.Origins[0].Session)
					}
				}
				if !found {
					t.Fatal("auto host missing")
				}
			}
			check()
			_, e = es.AppendActivity(Activity{Kind: "tool_use", Worker: actor, NodeID: node, MainSeg: &seg, Tool: "insert_assets", ToolUseID: call + "-repeat"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = as.RegisterAgentAssetWithOrigin(task.ID, actor, owner, call+"-repeat", write); e != nil {
				t.Fatal(e)
			}
			check()
			var serviceOrigin int64
			if e = d.QueryRow(`SELECT (source_origin->>'activity_id')::bigint FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, id).Scan(&serviceOrigin); e != nil || serviceOrigin != aid {
				t.Fatalf("direct asset: %d %v", serviceOrigin, e)
			}
			if _, e = d.Exec(`DELETE FROM activity WHERE id=$1`, aid); e != nil {
				t.Fatal(e)
			}
			groups, e := as.ListTaskAssetApprovalGroups(task.ID)
			if e != nil {
				t.Fatal(e)
			}
			for _, g := range groups {
				if g.Name == host && g.Origins[0].Available {
					t.Fatal("deleted activity still available")
				}
			}
		})
	}
	// A failed item rolls back its host and origin together.
	_, err = as.RegisterAgentAssetWithOrigin(task.ID, "planner", 0, "", func(s *AssetStore) (int64, error) {
		_, e := s.UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("rollback-%d.test", task.ID), TaskID: task.ID, AgentDiscovered: true})
		if e != nil {
			return 0, e
		}
		return 0, errors.New("rollback")
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	var n int
	if err = d.QueryRow(`SELECT count(*) FROM assets WHERE domain=$1`, fmt.Sprintf("rollback-%d.test", task.ID)).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback n=%d err=%v", n, err)
	}
}

func TestApprovalGroupOriginsAndRegistrationTime(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	source, err := d.CreateTask("group origins", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(source.ID)
	current, err := d.CreateTaskWithOptions("inherit origins", "goal", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(current.ID)
	other, err := d.CreateTask("unrelated origins", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	es := d.Exploration(source.ExplorationID)
	as := d.Assets()
	host := fmt.Sprintf("group-%d.test", source.ID)
	ids := []int64{}
	for i := range 2 {
		call := fmt.Sprintf("group-call-%d", i)
		if _, err = es.AppendActivity(Activity{Kind: "tool_use", Tool: "insert_assets", ToolUseID: call, Worker: "planner"}); err != nil {
			t.Fatal(err)
		}
		id, e := as.RegisterAgentAssetWithOrigin(source.ID, "planner", 0, call, func(s *AssetStore) (int64, error) {
			if i == 0 {
				return s.UpsertRootDomain(UpsertRootDomainReq{Domain: host, TaskID: source.ID, AgentDiscovered: true})
			}
			return s.UpsertSubdomain(UpsertSubdomainReq{Domain: host, RecordType: "CNAME", RecordValue: []string{"other.test"}, TaskID: source.ID, AgentDiscovered: true})
		})
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
	}
	if _, err = d.Exec(`UPDATE task_asset_links SET created_at='2020-01-01T00:00:00Z',approval_state='approved' WHERE task_id=$1 AND asset_id=$2`, source.ID, ids[0]); err != nil {
		t.Fatal(err)
	}
	groups, err := as.ListTaskAssetApprovalGroups(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	check := func(groups []TaskAssetApprovalGroup, inherited bool) {
		t.Helper()
		found := false
		for _, g := range groups {
			if g.Name != host {
				continue
			}
			found = true
			if len(g.Origins) != 2 || len(g.AssetIDs) != 2 || g.CreatedAt.Year() != 2020 || g.Inherited != inherited {
				t.Fatalf("bad group %+v", g)
			}
			for _, o := range g.Origins {
				if o.TaskID != source.ID || !o.Available {
					t.Fatalf("bad origin %+v", o)
				}
			}
		}
		if !found {
			t.Fatal("missing group")
		}
	}
	check(groups, false)
	inherited, err := as.ListTaskAssetApprovalGroups(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(inherited, true)
	unrelated, err := as.ListTaskAssetApprovalGroups(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("leaked source %+v", unrelated)
	}
	if _, err = d.Exec(`UPDATE task_asset_links SET approval_state='revoked' WHERE task_id=$1 AND asset_id=$2`, source.ID, ids[0]); err != nil {
		t.Fatal(err)
	}
	groups, err = as.ListTaskAssetApprovalGroups(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(groups, false)
}
