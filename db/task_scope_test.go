package db

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// TestStripHostPort covers the ip:port / [ipv6]:port stripping used by the
// ip/cidr scope path so a target like "10.0.188.136:3000" no longer 404s.
func TestStripHostPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"10.0.188.136:3000", "10.0.188.136"},       // the reported IP case
		{"10.0.188.136", "10.0.188.136"},            // bare IPv4 unchanged
		{"[2001:db8::1]:8080", "2001:db8::1"},       // bracketed IPv6 + port
		{"2001:db8::1", "2001:db8::1"},              // bare IPv6 unchanged (has colons)
		{" 1.2.3.4:80 ", "1.2.3.4"},                 // trims surrounding space
		{"example.com:443", "example.com"},          // domain + port → bare host
		{"api.example.com:8080", "api.example.com"}, // subdomain + port
		{"example.com", "example.com"},              // bare domain unchanged
	}
	for _, c := range cases {
		if got := stripHostPort(c.in); got != c.want {
			t.Errorf("stripHostPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestManualCompanyScopeApprovesExistingAssetsButKeywordDoesNot(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	task, err := d.CreateTask(fmt.Sprintf("manual company scope %d", suffix), "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	companyID, _, err := d.Companies().UpsertCompany(fmt.Sprintf("Manual Scope Company %d", suffix), "")
	if err != nil {
		t.Fatal(err)
	}
	companyAssetID, err := d.Assets().UpsertApp(UpsertAppReq{
		Name:      fmt.Sprintf("manual-company-app-%d", suffix),
		CompanyID: &companyID,
	})
	if err != nil {
		t.Fatal(err)
	}
	keywordAssetID, err := d.Assets().UpsertApp(UpsertAppReq{Name: fmt.Sprintf("keyword-only-%d", suffix)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(task.ID)
		_, _ = d.Assets().DeleteByIDs([]int64{companyAssetID, keywordAssetID})
		_ = d.Companies().DeleteCompany(companyID)
	})

	if _, err := d.Assets().AddAgentScope(task.ID, "company", strconv.FormatInt(companyID, 10), "用户确认企业范围", "manual"); err != nil {
		t.Fatal(err)
	}
	if err := d.Assets().ValidateTaskAssetsApproved(task.ID, []int64{companyAssetID}); err != nil {
		t.Fatalf("company asset not approved: %v", err)
	}
	if _, err := d.Assets().AddAgentScope(task.ID, "keyword", fmt.Sprintf("keyword-only-%d", suffix), "discovery hint", "manual"); err != nil {
		t.Fatal(err)
	}
	var keywordLinked bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_asset_links WHERE task_id=$1 AND asset_id=$2)`, task.ID, keywordAssetID).Scan(&keywordLinked); err != nil {
		t.Fatal(err)
	}
	if keywordLinked {
		t.Fatal("manual keyword scope claimed an asset by name")
	}
}
