package db

import (
	"fmt"
	"testing"
	"time"
)

func TestDerivedApprovalMigrationPreservesRevocations(t *testing.T) {
	dsn := testDSN(t)
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("derived approval migration", "migration", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	cases := []struct {
		name, assetType, state, actor, reason string
		blocked                               bool
	}{
		{"manual", "service", "revoked", "operator", "用户撤回", true},
		{"unknown", "endpoint", "revoked", "", "", true},
		{"inherited-reason", "service", "revoked", "operator", "继承父资产撤回", false},
		{"inherited-actor", "endpoint", "revoked", "inherited", "legacy reason", false},
		{"pending", "service", "pending", "", "Agent 发现，等待用户审批", false},
		{"existing-block", "endpoint", "revoked", "operator", "new reason", true},
	}
	ids := make([]int64, len(cases))
	times := make([]time.Time, len(cases))
	defer func() { _, _ = d.Assets().DeleteByIDs(ids) }()
	for i, tc := range cases {
		url := fmt.Sprintf("https://migration-%d.example/%s", task.ID, tc.name)
		if err := d.QueryRow(`INSERT INTO assets(type,url,method,task_ids) VALUES($1,$2,'GET',ARRAY[$3]::bigint[]) RETURNING id`, tc.assetType, url, task.ID).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
		if err := d.QueryRow(`UPDATE task_asset_links SET approval_state=$3,approved_by=$4,approval_reason=$5 WHERE task_id=$1 AND asset_id=$2 RETURNING updated_at`, task.ID, ids[i], tc.state, tc.actor, tc.reason).Scan(&times[i]); err != nil {
			t.Fatal(err)
		}
		if tc.name == "existing-block" {
			if _, err := d.Exec(`INSERT INTO task_asset_blocks(task_id,asset_key,asset_type,host_key,asset_id,reason,blocked_by,blocked_at) SELECT $1,task_asset_identity_key(a),a.type,task_asset_host(a),a.id,'original reason','original operator',$3 FROM assets a WHERE a.id=$2`, task.ID, ids[i], times[i].Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Reopening applies the actual startup schema, not only the helper function.
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	for i, tc := range cases {
		var state string
		if err := d.QueryRow(`SELECT approval_state FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, ids[i]).Scan(&state); err != nil || state != "approved" {
			t.Fatalf("%s state=%s err=%v", tc.name, state, err)
		}
		var count int
		if err := d.QueryRow(`SELECT count(*) FROM task_asset_blocks WHERE task_id=$1 AND asset_id=$2`, task.ID, ids[i]).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if (count == 1) != tc.blocked {
			t.Fatalf("%s tombstones=%d want blocked=%v", tc.name, count, tc.blocked)
		}
		if !tc.blocked {
			continue
		}
		var actor, reason string
		var blockedAt time.Time
		if err := d.QueryRow(`SELECT blocked_by,reason,blocked_at FROM task_asset_blocks WHERE task_id=$1 AND asset_id=$2`, task.ID, ids[i]).Scan(&actor, &reason, &blockedAt); err != nil {
			t.Fatal(err)
		}
		wantActor, wantReason, wantAt := tc.actor, tc.reason, times[i]
		if tc.name == "existing-block" {
			wantActor, wantReason, wantAt = "original operator", "original reason", times[i].Add(-time.Hour)
		} else if wantReason == "" {
			wantReason = "历史服务/接口撤回授权"
		}
		if actor != wantActor || reason != wantReason || !blockedAt.Equal(wantAt) {
			t.Fatalf("%s audit changed: actor=%q reason=%q time=%s", tc.name, actor, reason, blockedAt)
		}
	}
	// An explicit reattachment must survive subsequent startup migrations.
	if _, err := d.Assets().AttachAssetsToTask(task.ID, []int64{ids[0]}, "restore permission"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`SELECT normalize_derived_task_asset_approvals($1)`, task.ID); err != nil {
		t.Fatal(err)
	}
	var blocked bool
	if err := d.QueryRow(`SELECT task_asset_blocked($1,$2)`, task.ID, ids[0]).Scan(&blocked); err != nil || blocked {
		t.Fatalf("repeated migration recreated block=%v err=%v", blocked, err)
	}
}

func TestDerivedApprovalLegacyArchiveRestore(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTask("legacy derived archive", "restore migration", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
		_ = d.DeleteTask(task.ID)
	}()
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	var assetID int64
	if err := d.QueryRow(`INSERT INTO assets(type,url,task_ids) VALUES('service',$1,ARRAY[$2]::bigint[]) RETURNING id`, fmt.Sprintf("https://legacy-archive-%d.example", task.ID), task.ID).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = d.Assets().DeleteByIDs([]int64{assetID}) }()
	var revokedAt time.Time
	if err := d.QueryRow(`UPDATE task_asset_links SET approval_state='revoked',approved_by='archive operator',approval_reason='archive manual revoke' WHERE task_id=$1 AND asset_id=$2 RETURNING updated_at`, task.ID, assetID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/derived-legacy-test.tar.zst", "test", 100, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	var state, actor, reason string
	var blockedAt time.Time
	if err := d.QueryRow(`SELECT l.approval_state,b.blocked_by,b.reason,b.blocked_at FROM task_asset_links l JOIN task_asset_blocks b ON b.task_id=l.task_id AND b.asset_id=l.asset_id WHERE l.task_id=$1 AND l.asset_id=$2`, task.ID, assetID).Scan(&state, &actor, &reason, &blockedAt); err != nil {
		t.Fatal(err)
	}
	if state != "approved" || actor != "archive operator" || reason != "archive manual revoke" || !blockedAt.Equal(revokedAt) {
		t.Fatalf("restored revocation lost: state=%q actor=%q reason=%q time=%s", state, actor, reason, blockedAt)
	}
}
