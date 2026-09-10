package db

import (
	"fmt"
	"testing"
	"time"
)

func TestTaskCompanyPresetsBecomeApprovedAssets(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	host := fmt.Sprintf("user-provided-%d.test", time.Now().UnixNano())
	company, _, _, invalid, _, err := d.Companies().CreateCompanyWithScope(host, "", []ScopeInput{
		{Kind: "domain", Value: host},
		{Kind: "ip", Value: "2001:db8::917"},
		{Value: "https://" + host + "/api", Manual: true},
	}, "user preset")
	if err != nil || invalid != 0 {
		t.Fatalf("company: invalid=%d err=%v", invalid, err)
	}
	defer d.Exec(`DELETE FROM companies WHERE id=$1`, company)
	task, err := d.CreateTaskWithOptions("user-provided assets", "goal", TaskCreateOptions{CompanyIDs: []int64{company}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	assets, err := d.Assets().QueryByTask(task.ID, "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 {
		t.Fatalf("got %d assets, want domain, IP and HTTP service: %+v", len(assets), assets)
	}
	for _, a := range assets {
		if a.ApprovalState != ApprovalApproved || a.TaskSource != "company" {
			t.Fatalf("preset not user-approved: %+v", a)
		}
		if err := d.Assets().ValidateTaskAssetsApproved(task.ID, []int64{a.ID}); err != nil {
			t.Fatal(err)
		}
		state, err := d.Assets().RegisterAgentDiscoveredAsset(task.ID, a.ID, "worker")
		if err != nil || state != ApprovalApproved {
			t.Fatalf("rediscovery downgraded user asset: %s %v", state, err)
		}
	}
}
