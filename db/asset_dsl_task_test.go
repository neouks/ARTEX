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
	if !strings.Contains(blockedWhere, "task_asset_blocks") || strings.Contains(blockedWhere, "task_relations") {
		t.Fatalf("blocked where is not tombstone-only: %s", blockedWhere)
	}
}

func TestTaskDSLAppliesApprovalBeforePagination(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	stamp := time.Now().UnixNano()
	task, err := d.CreateTask(fmt.Sprintf("task-dsl-approval-%d", stamp), "test", nil, 0, 0)
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
}
