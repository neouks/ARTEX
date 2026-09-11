package db

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestTemplateArchiveAndInvalidQuarantine(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("测试 *.archive-approval.test", "goal", TaskCreateOptions{AssetApprovalTemplate: "related_assets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as := d.Assets()
	if _, err := as.RegisterDescriptionAsset(task.ID, "host", "*.archive-approval.test", "测试 *.archive-approval.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := as.UpsertSubdomain(UpsertSubdomainReq{
		Domain: "api.archive-approval.test", RecordType: "A", RecordValue: []string{"192.0.2.244"},
		TaskID: task.ID, AgentDiscovered: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-fix Agent row without using the now-validating Agent entry.
	bad, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: "www_host", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := as.SetTaskAssetSource(task.ID, bad, "agent", "legacy agent discovery", nil); err != nil {
		t.Fatal(err)
	}
	if err := as.BlockTaskAssets(task.ID, []int64{bad}, "user", "legacy manual block"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := d.Exec(`SELECT repair_task_asset_inputs($1)`, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	var blockKind string
	if err := d.QueryRow(`SELECT block_kind FROM task_asset_blocks WHERE task_id=$1 AND asset_id=$2`, task.ID, bad).Scan(&blockKind); err != nil || blockKind != "invalid" {
		t.Fatalf("invalid quarantine did not replace manual block: %q %v", blockKind, err)
	}
	if err := as.ApproveTaskAssets(task.ID, []int64{bad}, "user", "approve"); err == nil {
		t.Fatal("quarantine can be approved")
	}
	if _, err := as.AttachAssetsToTask(task.ID, []int64{bad}, "reattach"); err != nil {
		t.Fatal(err)
	}
	if err := as.ValidateTaskAssetsApproved(task.ID, []int64{bad}); err == nil {
		t.Fatal("reattach bypassed invalid quarantine")
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
	if snapshot.DataCounts["task_asset_grants"] == 0 {
		t.Fatal("missing archived grants")
	}
	if snapshot.DataCounts["task_asset_dns_evidence"] == 0 {
		t.Fatal("missing archived task-local DNS evidence")
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/template-fixture.tar.zst", "test", 100, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimTaskArchiveJob(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Old snapshots have no template: restoring must not use the new all-assets default.
	rows, err := decodeArchiveRows(snapshot.Tables["tasks"])
	if err != nil || len(rows) != 1 {
		t.Fatalf("task rows: %v %v", rows, err)
	}
	delete(rows[0], "asset_approval_template")
	snapshot.Tables["tasks"], err = json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	restored, err := d.GetTask(task.ID)
	if err != nil || restored.AssetApprovalTemplate != "explicit_targets" {
		t.Fatalf("legacy restore: %+v %v", restored, err)
	}
	if err := as.ValidateTaskAssetsApproved(task.ID, []int64{bad}); err == nil {
		t.Fatal("restore cleared quarantine")
	}
	var grants int
	if err := d.QueryRow(`SELECT count(*) FROM task_asset_grants WHERE task_id=$1`, task.ID).Scan(&grants); err != nil || grants == 0 {
		t.Fatalf("lost grants %d %v", grants, err)
	}
	var evidence int
	if err := d.QueryRow(`SELECT count(*) FROM task_asset_dns_evidence WHERE task_id=$1`, task.ID).Scan(&evidence); err != nil || evidence == 0 {
		t.Fatalf("lost task-local DNS evidence %d %v", evidence, err)
	}
}

func TestAssetApprovalTemplates(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, policy := range []string{"all_assets", "related_assets", "explicit_targets"} {
		t.Run(policy, func(t *testing.T) {
			task, err := d.CreateTaskWithOptions("测试 https://www.approval-fixture.test/", "goal", TaskCreateOptions{AssetApprovalTemplate: policy})
			if err != nil {
				t.Fatal(err)
			}
			defer d.DeleteTask(task.ID)
			as := d.Assets()
			provided, err := as.RegisterDescriptionAsset(task.ID, "host", "www.approval-fixture.test", "测试 https://www.approval-fixture.test/")
			if err != nil {
				t.Fatal(err)
			}
			if err := as.ValidateTaskAssetsApproved(task.ID, []int64{provided}); err != nil {
				t.Fatal(err)
			}
			child, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "api.approval-fixture.test", RecordType: "A", RecordValue: []string{"192.0.2.231"}, TaskID: task.ID, AgentDiscovered: true})
			if err != nil {
				t.Fatal(err)
			}
			unrelated, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: "unrelated-approval.test", TaskID: task.ID, AgentDiscovered: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, check := range []struct {
				id    int64
				allow bool
			}{{child, policy != "explicit_targets"}, {unrelated, policy == "all_assets"}} {
				state, err := as.RegisterAgentDiscoveredAsset(task.ID, check.id, "worker")
				if err != nil {
					t.Fatal(err)
				}
				if (state == ApprovalApproved) != check.allow {
					t.Fatalf("asset %d state=%s allow=%v", check.id, state, check.allow)
				}
			}
			var ip int64
			if err := d.QueryRow(`SELECT id FROM assets WHERE type='ip' AND ip='192.0.2.231'`).Scan(&ip); err != nil {
				t.Fatal(err)
			}
			if got := as.ValidateTaskAssetsApproved(task.ID, []int64{ip}) == nil; got != (policy != "explicit_targets") {
				t.Fatalf("DNS IP allowed=%v", got)
			}
			if err := as.BlockTaskAssets(task.ID, []int64{provided}, "user", "stop"); err != nil {
				t.Fatal(err)
			}
			if _, err := as.RegisterAgentDiscoveredAsset(task.ID, provided, "worker"); err == nil {
				t.Fatal("rediscovery removed block")
			}
			if err := as.ApproveTaskAssets(task.ID, []int64{provided}, "user", "resume"); err != nil {
				t.Fatal(err)
			}
			if err := as.RevokeTaskAssets(task.ID, []int64{provided}, "user", "stop"); err != nil {
				t.Fatal(err)
			}
			if _, err := as.RegisterAgentDiscoveredAsset(task.ID, provided, "worker"); err == nil {
				t.Fatal("rediscovery removed revocation")
			}
		})
	}
}

func TestHostExecutionTemplatesFailClosedAndDNSEvidenceIsTaskLocal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	as := d.Assets()
	for _, policy := range []string{"explicit_targets", "related_assets"} {
		task, err := d.CreateTaskWithOptions("host policy", "goal", TaskCreateOptions{AssetApprovalTemplate: policy})
		if err != nil {
			t.Fatal(err)
		}
		defer d.DeleteTask(task.ID)
		if err := as.ValidateTaskHostsApproved(task.ID, []string{"unknown-policy.test", "intranet"}); err == nil {
			t.Fatalf("%s allowed an unknown host", policy)
		}
	}
	openTask, err := d.CreateTaskWithOptions("open host policy", "goal", TaskCreateOptions{AssetApprovalTemplate: "all_assets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(openTask.ID)
	if err := as.ValidateTaskHostsApproved(openTask.ID, []string{"unknown-policy.test"}); err != nil {
		t.Fatalf("all-assets rejected a valid unknown host: %v", err)
	}

	owner, err := d.CreateTaskWithOptions("test https://www.local-evidence.test", "goal", TaskCreateOptions{AssetApprovalTemplate: "related_assets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(owner.ID)
	if _, err := as.RegisterDescriptionAsset(owner.ID, "host", "www.local-evidence.test", "test https://www.local-evidence.test"); err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateTaskWithOptions("other", "goal", TaskCreateOptions{AssetApprovalTemplate: "all_assets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	const discoveredIP = "192.0.2.242"
	if _, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "api.local-evidence.test", RecordType: "A", RecordValue: []string{discoveredIP}, TaskID: other.ID, AgentDiscovered: true}); err != nil {
		t.Fatal(err)
	}
	ipID, err := as.UpsertIP(UpsertIPReq{IP: discoveredIP, TaskID: owner.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as.RegisterAgentDiscoveredAsset(owner.ID, ipID, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := as.ValidateTaskAssetsApproved(owner.ID, []int64{ipID}); err == nil {
		t.Fatal("another task's DNS evidence authorized the IP")
	}
	if _, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "api.local-evidence.test", RecordType: "A", RecordValue: []string{discoveredIP}, TaskID: owner.ID, AgentDiscovered: true}); err != nil {
		t.Fatal(err)
	}
	if err := as.ValidateTaskAssetsApproved(owner.ID, []int64{ipID}); err != nil {
		t.Fatalf("same-task DNS evidence did not authorize the IP: %v", err)
	}
}

func TestAgentAssetValidationAndUserURL(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("validation", "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as := d.Assets()
	for _, host := range []string{"www_host", "${HOST}", "foo..example.test", "-bad.example.test", "host"} {
		if _, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: host, TaskID: task.ID, AgentDiscovered: true}); err == nil {
			t.Fatalf("accepted %q", host)
		}
	}
	for _, u := range []string{"https://www_host/x", "https://${HOST}/", "http://foo..test/"} {
		if _, err := as.UpsertHTTPService(UpsertHTTPServiceReq{URL: u, TaskID: task.ID, AgentDiscovered: true}); err == nil {
			t.Fatalf("accepted URL %q", u)
		}
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM task_asset_links WHERE task_id=$1`, task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid writes leaked: %d %v", count, err)
	}
	id, err := as.RegisterUserAsset(task.ID, func(scoped *AssetStore) (int64, error) {
		return scoped.UpsertHTTPService(UpsertHTTPServiceReq{URL: "https://www.manual-url.test/path", TaskID: task.ID})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := as.ValidateTaskAssetsApproved(task.ID, []int64{id}); err != nil {
		t.Fatal(err)
	}
	if _, err := as.RegisterAgentDiscoveredAsset(task.ID, id, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := as.ValidateTaskAssetsApproved(task.ID, []int64{id}); err != nil {
		t.Fatalf("user URL downgraded: %v", err)
	}
	if err := as.ValidateTaskHostsApproved(task.ID, []string{"www.manual-url.test"}); err != nil {
		t.Fatalf("tool layer blocked exact user host: %v", err)
	}
	apiID, err := as.RegisterUserAssetWithSource(task.ID, "api", "API exact provenance", func(scoped *AssetStore) (int64, error) {
		return scoped.UpsertRootDomain(UpsertRootDomainReq{Domain: "api-provenance.test", TaskID: task.ID})
	})
	if err != nil {
		t.Fatal(err)
	}
	var source, summary string
	if err := d.QueryRow(`SELECT source,source_summary FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, apiID).Scan(&source, &summary); err != nil {
		t.Fatal(err)
	}
	if source != "api" || summary != "API exact provenance" {
		t.Fatalf("lost user entry provenance: source=%q summary=%q", source, summary)
	}
}

func TestApprovalGroupsAtomicAndDurable(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("groups", "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as := d.Assets()
	host := fmt.Sprintf("dns-%d.approval.test", task.ID)
	var ids []int64
	for _, rt := range []string{"A", "AAAA"} {
		id, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: host, RecordType: rt, TaskID: task.ID, AgentDiscovered: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	key := fmt.Sprintf("%d|host:%s", task.ID, host)
	if err := as.ApproveTaskAssets(task.ID, nil, "user", "approve", key, "0|host:invalid.test"); err == nil {
		t.Fatal("mixed invalid groups succeeded")
	}
	if err := as.ValidateTaskAssetsApproved(task.ID, ids); err == nil {
		t.Fatal("partial approval persisted")
	}
	resolved, err := as.ApproveTaskAssetsResolved(task.ID, nil, "user", "approve", key)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != len(ids) {
		t.Fatalf("resolved group ids=%v, want %v", resolved, ids)
	}
	id, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: host, RecordType: "CNAME", TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := as.ValidateTaskAssetsApproved(task.ID, []int64{id}); err != nil {
		t.Fatalf("new DNS record lost approval: %v", err)
	}
	groups, err := as.ListTaskAssetApprovalGroups(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range groups {
		if g.GroupKey == key {
			found = true
			if len(g.AssetIDs) != 3 || len(g.RecordTypes) != 3 {
				t.Fatalf("group=%+v", g)
			}
		}
	}
	if !found {
		t.Fatal("missing group")
	}
	if err := as.RevokeTaskAssets(task.ID, nil, "user", "stop", key); err != nil {
		t.Fatal(err)
	}
	id, err = as.UpsertSubdomain(UpsertSubdomainReq{Domain: host, RecordType: "TXT", TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := as.ValidateTaskAssetsApproved(task.ID, []int64{id}); err == nil {
		t.Fatal("new DNS record bypassed revocation")
	}
}

func TestTemplateDefaultsAndDescriptionEvidence(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("测试 https://target.evidence.test；禁止测试 excluded.evidence.test", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	if task.AssetApprovalTemplate != "all_assets" {
		t.Fatal("wrong new default")
	}
	for _, entry := range []struct{ host, evidence string }{{"fake.evidence.test", "测试 https://target.evidence.test"}, {"evidence.test", "测试 https://target.evidence.test"}, {"excluded.evidence.test", "禁止测试 excluded.evidence.test"}} {
		if _, err := d.Assets().RegisterDescriptionAsset(task.ID, "host", entry.host, entry.evidence); err == nil {
			t.Fatalf("accepted ungrounded target %+v", entry)
		}
	}
	if _, err := d.Assets().RegisterDescriptionAsset(task.ID, "host", "excluded.evidence.test", "excluded.evidence.test"); err == nil {
		t.Fatal("partial quote bypassed denial")
	}
	if err := d.SetAssetApprovalTemplate(task.ID, "explicit_targets"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tasks SET first_run_at=now() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.SetAssetApprovalTemplate(task.ID, "all_assets"); err == nil {
		t.Fatal("changed template after start")
	}
}

func TestDNSOwnerMetadataNotHost(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("DNS metadata", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	id, err := d.Assets().UpsertSubdomain(UpsertSubdomainReq{Domain: "_ldap._tcp.dns-record.test", RecordType: "SRV", RecordValue: []string{"0 100 389 ldap.dns-record.test"}, TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	var domain string
	var count int
	if err := d.QueryRow(`SELECT domain,jsonb_array_length(extra->'dns_records') FROM assets WHERE id=$1`, id).Scan(&domain, &count); err != nil {
		t.Fatal(err)
	}
	if domain != "dns-record.test" || count != 1 {
		t.Fatalf("invalid DNS projection %s %d", domain, count)
	}
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE domain='_ldap._tcp.dns-record.test'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("DNS owner became a host: %d %v", count, err)
	}
}
