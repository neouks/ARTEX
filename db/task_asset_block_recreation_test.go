package db

import (
	"errors"
	"fmt"
	"testing"
)

func TestManualAssetBlockSurvivesGlobalRecreation(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("blocked identity", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	s := d.Assets()
	host := fmt.Sprintf("blocked-identity-%d.test", task.ID)
	id, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: host, TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BlockTaskAssets(task.ID, []int64{id}, "user", "identity block"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteByIDs([]int64{id}); err != nil {
		t.Fatal(err)
	}
	newID, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: host, TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if newID == id {
		t.Fatal("expected recreated identity")
	}
	if _, err := s.RegisterAgentDiscoveredAsset(task.ID, newID, "worker"); !errors.Is(err, ErrTaskAssetBlocked) {
		t.Fatalf("recreation bypassed block: %v", err)
	}
	rows, err := s.ListTaskAssetApprovals(task.ID)
	if err != nil || len(rows) != 1 || rows[0].AssetID != newID || rows[0].BlockKind != "manual" {
		t.Fatalf("duplicate or lost identity: %+v %v", rows, err)
	}
	if err := s.ApproveTaskAssets(task.ID, []int64{newID}, "user", "restore"); err != nil {
		t.Fatal(err)
	}
	state, err := s.RegisterAgentDiscoveredAsset(task.ID, newID, "worker")
	if err != nil || state != ApprovalApproved {
		t.Fatalf("rediscovery downgraded approval: %s %v", state, err)
	}
	if err := s.ValidateTaskAssetsApproved(task.ID, []int64{newID}); err != nil {
		t.Fatal(err)
	}
}
