package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDerivedApprovalControlsClaimAndTestedFromParent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("derived approval lifecycle", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	assets := d.Assets()
	host := fmt.Sprintf("derived-lifecycle-%d.test", time.Now().UnixNano())
	parentID, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: host, TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	serviceID, err := assets.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://" + host, TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := assets.UpsertEndpoint(UpsertEndpointReq{URL: "https://" + host + "/api", Method: "GET", TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	defer assets.DeleteByIDs([]int64{parentID, serviceID, endpointID})
	store := d.Exploration(task.ExplorationID)
	intentID, err := store.AddIntent(map[string]any{"summary": "derived lifecycle"}, 5, []int64{serviceID, endpointID}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	assertExecution := func(wantState string, allowed bool) {
		t.Helper()
		if _, err := d.Exec(`UPDATE task_asset_links SET tested=false WHERE task_id=$1 AND asset_id=ANY($2::bigint[])`, task.ID, []int64{serviceID, endpointID}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetNodeState(intentID, "open"); err != nil {
			t.Fatal(err)
		}
		claimed, err := store.ClaimIntent(intentID, "worker")
		if err != nil || claimed != allowed {
			t.Fatalf("parent=%s claim=%v err=%v; want %v", wantState, claimed, err, allowed)
		}
		if err := assets.MarkTaskAssetsTested(task.ID, []int64{serviceID, endpointID}, "worker"); err != nil {
			t.Fatal(err)
		}
		for _, id := range []int64{serviceID, endpointID} {
			var tested bool
			var storedState string
			if err := d.QueryRow(`SELECT approval_state,tested FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, id).Scan(&storedState, &tested); err != nil {
				t.Fatal(err)
			}
			if tested != allowed {
				t.Fatalf("parent=%s asset=%d tested=%v; want %v", wantState, id, tested, allowed)
			}
			if storedState != ApprovalApproved {
				t.Fatalf("parent changes must not copy %q to derived asset %d", storedState, id)
			}
		}
		filtered, err := assets.QueryByTaskApproval(task.ID, "", "all", wantState, 20, 0)
		if err != nil || !assetListContains(filtered, serviceID) || !assetListContains(filtered, endpointID) {
			t.Fatalf("effective %s filter omitted derived assets: %+v err=%v", wantState, filtered, err)
		}
	}
	assertExecution(ApprovalPending, false)
	if err := assets.ApproveTaskAssets(task.ID, []int64{parentID}, "operator", "approve parent"); err != nil {
		t.Fatal(err)
	}
	assertExecution(ApprovalApproved, true)
	if err := assets.RevokeTaskAssets(task.ID, []int64{parentID}, "operator", "revoke parent"); err != nil {
		t.Fatal(err)
	}
	assertExecution(ApprovalRevoked, false)
	if detached, err := assets.DetachAssetFromTask(task.ID, parentID); err != nil || !detached {
		t.Fatalf("detach parent=%v err=%v", detached, err)
	}
	if _, err := assets.AttachAssetsToTask(task.ID, []int64{serviceID, endpointID}, "reattach derived"); err != nil {
		t.Fatal(err)
	}
	if err := assets.ValidateTaskAssetsApproved(task.ID, []int64{serviceID, endpointID}); !errors.Is(err, ErrTaskAssetBlocked) {
		t.Fatalf("reattaching derived cleared parent tombstone: %v", err)
	}
	if claimed, err := store.ClaimIntent(intentID, "worker"); err != nil || claimed {
		t.Fatalf("deleted parent claim=%v err=%v", claimed, err)
	}
}
