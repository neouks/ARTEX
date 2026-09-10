package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestManualAssetBlockLifecycle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := d.Assets()
	for _, initial := range []string{ApprovalPending, ApprovalApproved, ApprovalRevoked} {
		t.Run(initial, func(t *testing.T) {
			task, err := d.CreateTask("block lifecycle", "goal", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer d.DeleteTask(task.ID)
			host := fmt.Sprintf("block-%d-%s.test", task.ID, initial)
			root, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: host, TaskID: task.ID})
			if err != nil {
				t.Fatal(err)
			}
			child, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://" + host, TaskID: task.ID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(`UPDATE task_asset_links SET approval_state=$3,tested=true WHERE task_id=$1 AND asset_id=$2`, task.ID, root, initial); err != nil {
				t.Fatal(err)
			}
			if err := s.BlockTaskAssets(task.ID, []int64{root, child}, "operator", ""); !errors.Is(err, ErrTaskAssetInvalid) {
				t.Fatalf("mixed derived request: %v", err)
			}
			var state string
			if err := d.QueryRow(`SELECT approval_state FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, root).Scan(&state); err != nil || state != initial {
				t.Fatalf("partial write: %s %v", state, err)
			}
			if err := s.BlockTaskAssets(task.ID, []int64{root}, "operator", ""); err != nil {
				t.Fatal(err)
			}
			var first time.Time
			if err := d.QueryRow(`SELECT blocked_at FROM task_asset_blocks WHERE task_id=$1 AND asset_id=$2`, task.ID, root).Scan(&first); err != nil {
				t.Fatal(err)
			}
			if err := s.BlockTaskAssets(task.ID, []int64{root}, "other", "again"); err != nil {
				t.Fatal(err)
			}
			var at time.Time
			var actor, kind, reason string
			if err := d.QueryRow(`SELECT blocked_at,blocked_by,block_kind,reason FROM task_asset_blocks WHERE task_id=$1 AND asset_id=$2`, task.ID, root).Scan(&at, &actor, &kind, &reason); err != nil {
				t.Fatal(err)
			}
			if !first.Equal(at) || actor != "operator" || kind != "manual" || reason != "用户封禁测试授权" {
				t.Fatalf("audit lost: %s %s %s %s", at, actor, kind, reason)
			}
			if _, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://" + host, TaskID: task.ID, AgentDiscovered: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RegisterAgentDiscoveredAsset(task.ID, child, "worker"); !errors.Is(err, ErrTaskAssetBlocked) {
				t.Fatalf("rediscovery bypass: %v", err)
			}
			var count int
			if err := d.QueryRow(`SELECT count(*) FROM task_asset_links WHERE task_id=$1 AND asset_id=ANY($2::bigint[])`, task.ID, []int64{root, child}).Scan(&count); err != nil || count != 2 {
				t.Fatalf("block detached records: %d %v", count, err)
			}
			rows, err := s.QueryByTaskApproval(task.ID, "", "all", "blocked", 20, 0)
			if err != nil || len(rows) != 2 {
				t.Fatalf("blocked query: %v %v", rows, err)
			}
			for _, a := range rows {
				if a.ApprovalState != ApprovalBlocked || !a.Blocked {
					t.Fatalf("wrong DTO: %+v", a)
				}
			}
			approvals, err := s.ListTaskAssetApprovals(task.ID)
			if err != nil || len(approvals) != 1 || !approvals[0].BlockDirect || approvals[0].BlockKind != "manual" {
				t.Fatalf("approval DTO: %+v %v", approvals, err)
			}
			if err := s.ValidateTaskHostsApproved(task.ID, []string{host}); !errors.Is(err, ErrTaskAssetBlocked) {
				t.Fatalf("host bypass: %v", err)
			}
			intent, err := d.Exploration(task.ExplorationID).AddIntent(map[string]any{"summary": "blocked child"}, 5, []int64{child}, "planner")
			if err != nil {
				t.Fatal(err)
			}
			if claimed, err := d.Exploration(task.ExplorationID).ClaimIntent(intent, "worker"); err != nil || claimed {
				t.Fatalf("claim bypass: %v %v", claimed, err)
			}
			if err := s.MarkTaskAssetsTested(task.ID, []int64{child}, "worker"); err != nil {
				t.Fatal(err)
			}
			var tested bool
			if err := d.QueryRow(`SELECT tested FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, child).Scan(&tested); err != nil || tested {
				t.Fatalf("blocked child tested: %v %v", tested, err)
			}
			if err := s.ApproveTaskAssets(task.ID, []int64{root}, "operator", "restore"); err != nil {
				t.Fatal(err)
			}
			if err := s.ValidateTaskAssetsApproved(task.ID, []int64{root, child}); err != nil {
				t.Fatal(err)
			}
			if err := d.QueryRow(`SELECT tested FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, root).Scan(&tested); err != nil || !tested {
				t.Fatalf("test history erased: %v %v", tested, err)
			}
			if _, err := s.DetachAssetFromTask(task.ID, root); err != nil {
				t.Fatal(err)
			}
			if err := s.ApproveTaskAssets(task.ID, []int64{root}, "operator", ""); err == nil {
				t.Fatal("deleted asset approved without reattachment")
			}
			if _, err := s.AttachAssetsToTask(task.ID, []int64{root}, "restore deletion"); err != nil {
				t.Fatal(err)
			}
			if err := s.ValidateTaskAssetsApproved(task.ID, []int64{root, child}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManualAssetBlockArchiveCompatibility(t *testing.T) {
	dsn := testDSN(t)
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("block archive", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	root, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("block-archive-%d.test", task.ID), TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Assets().BlockTaskAssets(task.ID, []int64{root}, "operator", "keep audit"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	var raw []byte
	if err := d.QueryRow(`SELECT json_agg(b) FROM task_asset_blocks b WHERE task_id=$1`, task.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		tx, err := d.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM task_asset_blocks WHERE task_id=$1`, task.ID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		payload := raw
		if legacy {
			var records []map[string]any
			if err := json.Unmarshal(raw, &records); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			for _, record := range records {
				delete(record, "block_kind")
			}
			payload, err = json.Marshal(records)
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
		}
		if err := insertArchiveRows(tx, "task_asset_blocks", payload); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		var kind string
		if err := tx.QueryRow(`SELECT block_kind FROM task_asset_blocks WHERE task_id=$1`, task.ID).Scan(&kind); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		want := "manual"
		if legacy {
			want = "deleted"
		}
		if kind != want {
			tx.Rollback()
			t.Fatalf("kind=%s want %s", kind, want)
		}
		tx.Rollback()
	}
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM task_archives WHERE id=$1`, archive.ID)
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/manual-block-test.tar.zst", "test", 100, 50); err != nil {
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
	var state, kind string
	if err := d.QueryRow(`SELECT l.approval_state,b.block_kind FROM task_asset_links l JOIN task_asset_blocks b
ON b.task_id=l.task_id AND b.asset_id=l.asset_id WHERE l.task_id=$1 AND l.asset_id=$2`, task.ID, root).Scan(&state, &kind); err != nil {
		t.Fatal(err)
	}
	if state != ApprovalBlocked || kind != "manual" {
		t.Fatalf("archive lost block: %s %s", state, kind)
	}
	if err := d.Assets().ApproveTaskAssets(task.ID, []int64{root}, "user", "restore"); err != nil {
		t.Fatal(err)
	}
}
