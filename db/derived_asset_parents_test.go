package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDerivedManualParentAndAgentTransaction(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("derived parent transaction", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	s := d.Assets()
	host := fmt.Sprintf("derived-parent-%d.test", time.Now().UnixNano())
	child, err := s.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://" + host})
	if err != nil {
		t.Fatal(err)
	}
	// A legacy child-only association is not an execution grant.
	if _, err := d.Exec(`UPDATE assets SET task_ids=array_append(task_ids,$1) WHERE id=$2`, task.ID, child); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateTaskAssetsApproved(task.ID, []int64{child}); !errors.Is(err, ErrTaskAssetNotApproved) {
		t.Fatalf("missing parent did not fail closed: %v", err)
	}
	if _, err := s.AttachAssetsToTask(task.ID, []int64{child}, "manual service"); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateTaskAssetsApproved(task.ID, []int64{child}); err != nil {
		t.Fatalf("manual child must get a usable parent: %v", err)
	}
	var parent int64
	if err := d.QueryRow(`SELECT id FROM assets WHERE type='root_domain' AND domain=$1`, host).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DetachAssetFromTask(task.ID, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttachAssetsToTask(task.ID, []int64{child}, "reattach child only"); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateTaskAssetsApproved(task.ID, []int64{child}); !errors.Is(err, ErrTaskAssetBlocked) {
		t.Fatalf("reattaching child bypassed parent tombstone: %v", err)
	}
	if _, err := s.UpsertEndpoint(UpsertEndpointReq{URL: "https://[2001:db8::913]/api", Method: "GET", TaskID: task.ID, AgentDiscovered: true}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := d.QueryRow(`SELECT l.approval_state FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
WHERE l.task_id=$1 AND a.type='ip' AND a.ip='2001:db8::913'`, task.ID).Scan(&state); err != nil || state != ApprovalPending {
		t.Fatalf("atomic IP parent state=%s err=%v", state, err)
	}
	if _, err := s.UpsertEndpoint(UpsertEndpointReq{URL: "missing-host", Method: "GET", TaskID: task.ID, AgentDiscovered: true}); err == nil {
		t.Fatal("agent endpoint without a host was accepted")
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE type='endpoint' AND url='missing-host' AND $1=ANY(task_ids)`, task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed discovery left partial child: count=%d err=%v", count, err)
	}
}
