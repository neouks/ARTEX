package db

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBuildTaskDSLWhereKeepsPlaceholderOrderAndAuthorization(t *testing.T) {
	where, args, err := buildTaskDSLWhere(42, "domain=example.test", "service", "false", ApprovalApproved)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"type=$2",
		"current_link.task_id=$3",
		"relation.task_id=$3",
		"current_link.tested",
		"source_link.tested",
		"task_asset_effectively_approved($3,assets.id)",
	} {
		if !strings.Contains(where, want) {
			t.Fatalf("where missing %q: %s", want, where)
		}
	}
	if len(args) != 4 || args[1] != "service" || args[2] != int64(42) || args[3] != false {
		t.Fatalf("unexpected args: %#v", args)
	}

	blockedWhere, _, err := buildTaskDSLWhere(42, "domain=example.test", "", "all", "blocked")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blockedWhere, "task_asset_effective_approval_state($2,assets.id)='blocked'") {
		t.Fatalf("blocked where does not use effective tombstones: %s", blockedWhere)
	}
}

func TestTaskDSLAppliesApprovalBeforePagination(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	stamp := time.Now().UnixNano()
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("task-dsl-approval-%d", stamp), "test", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	marker := fmt.Sprintf("task-dsl-page-%d", stamp)
	approvedID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: marker + "-approved.invalid", TaskID: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	pendingID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: marker + "-pending.invalid", TaskID: task.ID, AgentDiscovered: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs([]int64{approvedID, pendingID}) })

	assets, err := d.Assets().QueryDSLByTaskApproval(task.ID, marker, "root_domain", "all", ApprovalApproved, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].ID != approvedID {
		t.Fatalf("approved page=%+v, want only %d", assets, approvedID)
	}
	count, err := d.Assets().CountDSLByTaskApproval(task.ID, marker, "root_domain", "all", ApprovalApproved)
	if err != nil || count != 1 {
		t.Fatalf("approved count=%d err=%v, want 1", count, err)
	}
	for _, clause := range []string{"status==approved", "approval_state==approved", "(approval_state==pending OR approval_state==approved)"} {
		rows, err := d.Assets().QueryDSLByTaskApproval(task.ID, marker+" AND "+clause, "root_domain", "all", ApprovalApproved, 20, 0)
		if err != nil || len(rows) != 1 || rows[0].ID != approvedID {
			t.Fatalf("%s: rows=%v err=%v", clause, rows, err)
		}
	}
}

func TestTaskDSLApprovalFields(t *testing.T) {
	for _, field := range []string{"status", "approval_state"} {
		for _, state := range []string{"approved", "pending", "blocked", "revoked"} {
			where, args, err := buildTaskDSLWhere(42, field+"=="+state, "", "all", ApprovalApproved)
			if err != nil || !strings.Contains(where, "task_asset_effective_approval_state($1,assets.id) = $2") || !strings.Contains(where, "task_asset_effectively_approved($3,assets.id)") {
				t.Fatalf("%s=%s: %s %v", field, state, where, err)
			}
			if args[0] != int64(42) || args[1] != state {
				t.Fatalf("args=%v", args)
			}
		}
	}
	for _, query := range []string{"status==200", "status_code==200"} {
		where, _, err := buildTaskDSLWhere(42, query, "", "all", ApprovalApproved)
		if err != nil || !strings.Contains(where, "status_code = $1") {
			t.Fatalf("%s: %s %v", query, where, err)
		}
	}
	for _, query := range []string{"approval_state==unknown", "approval_state>approved", "status>approved"} {
		if _, _, err := buildTaskDSLWhere(42, query, "", "all", ApprovalApproved); err == nil {
			t.Fatal("accepted", query)
		}
	}
	for _, query := range []string{"approval_state==approved", "status==approved", "task_id==42 AND status==approved"} {
		n, err := ParseDSL(query)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := buildDSLWhere(n); err == nil {
			t.Fatal("global query accepted approval filter", query)
		}
	}
	if _, _, err := buildTaskDSLWhere(42, "", "", "all", ApprovalApproved); err != nil {
		t.Fatal(err)
	}
}
