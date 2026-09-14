package db

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"
)

func TestTaskNotificationsVisibilityAndGrouping(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("notifications", "", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	q := TaskNotificationQuery{TaskID: strconv.FormatInt(task.ID, 10)}
	query := func(only bool) *TaskNotificationSummary {
		t.Helper()
		r, e := d.TaskNotifications(t.Context(), []TaskNotificationQuery{q}, only)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := d.Exec(sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO findings(task_id,summary) VALUES($1,'historical')`, task.ID)
	initial := query(false)
	if initial.Items[0].Findings != 0 {
		t.Fatal("first visit must establish baseline")
	}
	q.Findings, q.Assets, q.Intercepts = initial.Snapshot, initial.Snapshot, initial.Snapshot
	// Allocate the creation transaction BEFORE the read snapshot, commit AFTER it.
	tx, e := d.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`INSERT INTO findings(task_id,summary) VALUES($1,'late commit')`, task.ID); e != nil {
		t.Fatal(e)
	}
	beforeCommit := query(false)
	q.Findings = beforeCommit.Snapshot
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if got := query(false).Items[0].Findings; got != 1 {
		t.Fatalf("late commit lost: %d", got)
	}
	exec(`UPDATE findings SET summary='edited' WHERE task_id=$1`, task.ID)
	if got := query(false).Items[0].Findings; got != 1 {
		t.Fatalf("edit changed unread: %d", got)
	}
	s := d.Assets()
	host := fmt.Sprintf("notify-%d.test", task.ID)
	id, e := s.UpsertRootDomain(UpsertRootDomainReq{Domain: host, TaskID: task.ID, AgentDiscovered: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.UpsertSubdomain(UpsertSubdomainReq{Domain: host, RecordType: "TXT", RecordValue: []string{"test"}, TaskID: task.ID, AgentDiscovered: true}); e != nil {
		t.Fatal(e)
	}
	exec(`INSERT INTO intercept_pending(task_id,tool_name) VALUES($1,'Bash')`, q.TaskID)
	r := query(false)
	if r.Items[0].Assets != 1 || r.Items[0].Intercepts != 1 {
		t.Fatalf("grouped counts: %+v", r)
	}
	if r := query(true); r.Items[0].Assets != 0 || r.Items[0].Intercepts != 0 {
		t.Fatal("task list leaked approval counts")
	}
	q.Findings, q.Assets, q.Intercepts = r.Snapshot, r.Snapshot, r.Snapshot
	if r := query(false); r.Items[0].Findings+r.Items[0].Assets+r.Items[0].Intercepts != 0 {
		t.Fatalf("read: %+v", r)
	}
	if _, e = s.UpsertSubdomain(UpsertSubdomainReq{Domain: host, RecordType: "AAAA", RecordValue: []string{"::1"}, TaskID: task.ID, AgentDiscovered: true}); e != nil {
		t.Fatal(e)
	}
	if got := query(false).Items[0].Assets; got != 1 { // New side-effect IP, not the existing DNS group.
		t.Fatalf("expected only new IP, got %d", got)
	}
	if e = s.ApproveTaskAssets(task.ID, []int64{id}, "user", ""); e != nil {
		t.Fatal(e)
	}
	exec(`UPDATE intercept_pending SET status='allowed' WHERE task_id=$1`, q.TaskID)
	q.Intercepts = initial.Snapshot
	if query(false).Items[0].Intercepts != 0 {
		t.Fatal("processed intercept is still unread")
	}
	// Migration metadata is immutable and legacy NULLs stay historical.
	exec(`UPDATE findings SET notification_xid=NULL WHERE task_id=$1`, task.ID)
	d2, e := Open(testDSN(t))
	if e != nil {
		t.Fatal(e)
	}
	d2.Close()
	q.Findings = initial.Snapshot
	if query(false).Items[0].Findings != 0 {
		t.Fatal("migration backfilled historical records")
	}
}

func TestTaskNotificationsBatchIsolation(t *testing.T) {
	d, e := Open(testDSN(t))
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	queries := make([]TaskNotificationQuery, 0, 20)
	for range 20 {
		task, e := d.CreateTaskWithOptions("notification batch", "", TaskCreateOptions{})
		if e != nil {
			t.Fatal(e)
		}
		defer d.DeleteTask(task.ID)
		queries = append(queries, TaskNotificationQuery{TaskID: strconv.FormatInt(task.ID, 10)})
	}
	base, e := d.TaskNotifications(t.Context(), queries, true)
	if e != nil {
		t.Fatal(e)
	}
	for i := range queries {
		queries[i].Findings = base.Snapshot
	}
	if _, e = d.Exec(`INSERT INTO findings(task_id) SELECT $1::bigint FROM generate_series(1,120)`, queries[0].TaskID); e != nil {
		t.Fatal(e)
	}
	started := time.Now()
	r, e := d.TaskNotifications(t.Context(), queries, true)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(r)
	t.Logf("20-task batch, 120 new findings: one SQL statement, %d response bytes, %s", len(raw), time.Since(started))
	for _, row := range r.Items {
		want := 0
		if row.TaskID == queries[0].TaskID {
			want = 120
		}
		if row.Findings != want {
			t.Fatalf("isolation %+v", row)
		}
	}
}

func TestTaskNotificationsPendingOnlyAndSources(t *testing.T) {
	d, e := Open(testDSN(t))
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	source, e := d.CreateTaskWithOptions("source", "", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if e != nil {
		t.Fatal(e)
	}
	defer d.DeleteTask(source.ID)
	task, e := d.CreateTaskWithOptions("child", "", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets", SourceTaskIDs: []int64{source.ID}})
	if e != nil {
		t.Fatal(e)
	}
	defer d.DeleteTask(task.ID)
	q := []TaskNotificationQuery{{TaskID: strconv.FormatInt(task.ID, 10)}}
	base, e := d.TaskNotifications(t.Context(), q, false)
	if e != nil {
		t.Fatal(e)
	}
	q[0].Assets = base.Snapshot
	s := d.Assets()
	add := func(owner int64, name string, discovered bool) int64 {
		t.Helper()
		id, e := s.UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("%s-%d.test", name, task.ID), TaskID: owner, AgentDiscovered: discovered})
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	add(source.ID, "inherited", true)
	add(task.ID, "user", false)
	id := add(task.ID, "pending", true)
	check := func(want int) {
		t.Helper()
		r, e := d.TaskNotifications(t.Context(), q, false)
		if e != nil {
			t.Fatal(e)
		}
		if r.Items[0].Assets != want {
			t.Fatalf("counts %+v", r)
		}
	}
	check(1)
	if e = s.ApproveTaskAssets(task.ID, []int64{id}, "user", ""); e != nil {
		t.Fatal(e)
	}
	check(0)
	id = add(task.ID, "delete", true)
	check(1)
	if _, e = s.DetachAssetFromTask(task.ID, id); e != nil {
		t.Fatal(e)
	}
	check(0)
	id = add(task.ID, "block", true)
	if e = s.BlockTaskAssets(task.ID, []int64{id}, "user", ""); e != nil {
		t.Fatal(e)
	}
	check(0)
}
