package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRegisterTaskAssetScopesCreatesAssetsAndPersistsTextScope(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	task, err := d.CreateTask("manual scope registration", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	suffix := time.Now().UnixNano()
	domain := fmt.Sprintf("manual-scope-%d.example.test", suffix)
	ip := fmt.Sprintf("fc%02x:%x:%x::19", byte(suffix), uint16(suffix>>8), uint16(suffix>>24))
	cidrPrefix := fmt.Sprintf("fd%02x:%x:%x", byte(suffix), uint16(suffix>>16), uint16(suffix>>32))
	cidr := cidrPrefix + "::/64"
	cidrIP := cidrPrefix + "::23"
	icp := fmt.Sprintf("京 ICP 备 %d 号-1", suffix)
	cidrAssetID, err := d.Assets().UpsertIP(UpsertIPReq{IP: cidrIP})
	if err != nil {
		t.Fatal(err)
	}
	icpAssetID, err := d.Assets().UpsertApp(UpsertAppReq{Name: fmt.Sprintf("manual ICP app %d", suffix), ICP: icp})
	if err != nil {
		t.Fatal(err)
	}
	keywordAssetID, err := d.Assets().UpsertApp(UpsertAppReq{Name: fmt.Sprintf("Acme Security %d", suffix)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Assets().AttachAssetsToTask(task.ID, []int64{cidrAssetID}, "prepare tombstone"); err != nil {
		t.Fatal(err)
	}
	if detached, err := d.Assets().DetachAssetFromTask(task.ID, cidrAssetID); err != nil || !detached {
		t.Fatalf("prepare cidr tombstone: detached=%v err=%v", detached, err)
	}
	inputs := []ScopeInput{
		{Kind: "domain", Value: domain},
		{Kind: "ip", Value: ip},
		{Kind: "cidr", Value: cidr},
		{Kind: "icp", Value: " " + icp + " "},
		{Kind: "keyword", Value: fmt.Sprintf(" Acme   Security   %d ", suffix)},
	}

	first, err := d.Assets().RegisterTaskAssetScopes(task.ID, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if first.Requested != 5 || first.AssetsLinked != 2 || first.AssetsExisting != 0 || first.ScopesAdded != 5 || first.ScopesExisting != 0 {
		t.Fatalf("unexpected first mutation: %+v", first)
	}
	assets, err := d.Assets().QueryByTask(task.ID, "", 10, 0)
	if err != nil || len(assets) != 4 {
		t.Fatalf("task assets=%+v err=%v", assets, err)
	}
	assetIDs := make([]int64, 0, len(assets))
	linked := make(map[int64]bool, len(assets))
	for _, asset := range assets {
		assetIDs = append(assetIDs, asset.ID)
		linked[asset.ID] = true
		if asset.TaskSource != "manual" || asset.TaskSourceSummary != manualTaskScopeSummary {
			t.Fatalf("unexpected task asset provenance: %+v", asset)
		}
		if asset.ApprovalState != ApprovalApproved {
			t.Fatalf("manual scope asset was not approved: %+v", asset)
		}
	}
	if !linked[cidrAssetID] || !linked[icpAssetID] || linked[keywordAssetID] {
		t.Fatalf("scope authorization mismatch: cidr=%v icp=%v keyword=%v", linked[cidrAssetID], linked[icpAssetID], linked[keywordAssetID])
	}
	var cidrBlocks int
	if err := d.QueryRow(`SELECT count(*) FROM task_asset_blocks WHERE task_id=$1 AND asset_id=$2`, task.ID, cidrAssetID).Scan(&cidrBlocks); err != nil {
		t.Fatal(err)
	}
	if cidrBlocks != 0 {
		t.Fatalf("manual CIDR registration left %d tombstones", cidrBlocks)
	}
	assetIDs = append(assetIDs, keywordAssetID)
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs(assetIDs) })
	scopes, err := d.Assets().ListTaskScope(task.ID)
	if err != nil || len(scopes) != 5 {
		t.Fatalf("task scope=%+v err=%v", scopes, err)
	}
	values := map[string]string{}
	for _, scope := range scopes {
		values[scope.Kind] = scope.Value
	}
	if values["icp"] != NormalizeICP(inputs[3].Value) || values["keyword"] != strings.TrimSpace(inputs[4].Value) {
		t.Fatalf("text scope not normalized: %+v", values)
	}

	second, err := d.Assets().RegisterTaskAssetScopes(task.ID, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if second.AssetsExisting != 2 || second.AssetsLinked != 0 || second.ScopesExisting != 5 || second.ScopesAdded != 0 {
		t.Fatalf("unexpected idempotent mutation: %+v", second)
	}

	rollbackDomain := fmt.Sprintf("rollback-%d.example.test", suffix)
	_, err = d.Assets().RegisterTaskAssetScopes(task.ID, []ScopeInput{
		{Kind: "domain", Value: rollbackDomain},
		{Kind: "keyword", Value: strings.Repeat("x", MaxCompanyScopeRawRunes+1)},
	})
	if !errors.Is(err, ErrTaskAssetInvalid) {
		t.Fatalf("oversized scope error=%v", err)
	}
	var rollbackAssets int
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE type='root_domain' AND domain=$1`, rollbackDomain).Scan(&rollbackAssets); err != nil {
		t.Fatal(err)
	}
	if rollbackAssets != 0 {
		t.Fatalf("invalid request created %d assets", rollbackAssets)
	}
}

func TestTaskAssetAttachDetachPreservesGlobalAssetAndAnchors(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	task, err := d.CreateTask("asset editing", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("manual-%d.example.test", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs([]int64{assetID}) })

	mutation, err := d.Assets().AttachAssetsToTask(task.ID, []int64{assetID, assetID}, "授权资产清单第 3 项")
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Requested != 1 || mutation.Attached != 1 || mutation.Existing != 0 {
		t.Fatalf("unexpected first mutation: %+v", mutation)
	}
	assets, err := d.Assets().QueryByTask(task.ID, "", 10, 0)
	if err != nil || len(assets) != 1 {
		t.Fatalf("task assets=%+v err=%v", assets, err)
	}
	if assets[0].TaskSource != "manual" || assets[0].TaskSourceSummary != "授权资产清单第 3 项" {
		t.Fatalf("unexpected provenance: %+v", assets[0])
	}

	intentID, err := d.Exploration(task.ExplorationID).AddIntent(map[string]any{"summary": "test asset"}, 5, []int64{assetID}, "human")
	if err != nil {
		t.Fatal(err)
	}
	detached, err := d.Assets().DetachAssetFromTask(task.ID, assetID)
	if err != nil || !detached {
		t.Fatalf("detach=%v err=%v", detached, err)
	}
	if assets, err := d.Assets().QueryByTask(task.ID, "", 10, 0); err != nil || len(assets) != 1 || !assets[0].Blocked {
		t.Fatalf("detached task assets must retain a visible tombstone: assets=%+v err=%v", assets, err)
	}
	var global, anchors, links int
	_ = d.QueryRow(`SELECT count(*) FROM assets WHERE id=$1`, assetID).Scan(&global)
	_ = d.QueryRow(`SELECT count(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id=$2`, intentID, assetID).Scan(&anchors)
	_ = d.QueryRow(`SELECT count(*) FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, assetID).Scan(&links)
	if global != 1 || anchors != 1 || links != 0 {
		t.Fatalf("global=%d anchors=%d links=%d", global, anchors, links)
	}
}

func TestTaskAssetApprovalInheritanceAndTombstone(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("approval-%d", suffix), "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	assets := d.Assets()

	rootID, err := assets.UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("approval-%d.test", suffix), TaskID: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := assets.RegisterAgentDiscoveredAsset(task.ID, rootID, "worker")
	if err != nil || state != ApprovalPending {
		t.Fatalf("agent root state=%q err=%v, want pending", state, err)
	}
	if err := assets.ValidateTaskAssetsApproved(task.ID, []int64{rootID}); !errors.Is(err, ErrTaskAssetNotApproved) {
		t.Fatalf("pending root validation=%v, want ErrTaskAssetNotApproved", err)
	}

	serviceID, err := assets.UpsertHTTPService(UpsertHTTPServiceReq{
		URL: fmt.Sprintf("https://approval-%d.test/", suffix), TaskID: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = assets.RegisterAgentDiscoveredAsset(task.ID, serviceID, "worker")
	if err != nil || state != ApprovalPending {
		t.Fatalf("service inherited state=%q err=%v, want pending", state, err)
	}
	pendingAssets, err := assets.QueryByTaskApproval(task.ID, "", "all", ApprovalPending, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !assetListContains(pendingAssets, rootID) || !assetListContains(pendingAssets, serviceID) {
		t.Fatalf("pending filter omitted effective child state: %+v", pendingAssets)
	}
	if err := assets.ApproveTaskAssets(task.ID, []int64{serviceID}, "operator", "child only"); !errors.Is(err, ErrTaskAssetInvalid) {
		t.Fatalf("child-only approval=%v, want ErrTaskAssetInvalid", err)
	}

	if err := assets.ApproveTaskAssets(task.ID, []int64{rootID}, "operator", "approved for test"); err != nil {
		t.Fatal(err)
	}
	// Services read their parent's current decision without rediscovery or a
	// separate child approval.
	if err := assets.ValidateTaskAssetsApproved(task.ID, []int64{serviceID}); err != nil {
		t.Fatalf("service after parent approval=%v", err)
	}
	if err := d.QueryRow(`SELECT approval_state FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, serviceID).Scan(&state); err != nil || state != ApprovalApproved {
		t.Fatalf("stored service state=%q err=%v, want approved", state, err)
	}
	if err := assets.RevokeTaskAssets(task.ID, []int64{rootID}, "operator", "temporary withdrawal"); err != nil {
		t.Fatal(err)
	}
	if err := assets.ValidateTaskAssetsApproved(task.ID, []int64{serviceID}); !errors.Is(err, ErrTaskAssetNotApproved) {
		t.Fatalf("service after parent revoke=%v, want ErrTaskAssetNotApproved", err)
	}
	revokedAssets, err := assets.QueryByTaskApproval(task.ID, "", "all", ApprovalRevoked, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !assetListContains(revokedAssets, rootID) || !assetListContains(revokedAssets, serviceID) {
		t.Fatalf("revoked filter omitted effective child state: %+v", revokedAssets)
	}
	if err := assets.ApproveTaskAssets(task.ID, []int64{rootID}, "operator", "restore"); err != nil {
		t.Fatal(err)
	}
	if err := assets.ValidateTaskAssetsApproved(task.ID, []int64{serviceID}); err != nil {
		t.Fatalf("service after parent reapproval=%v", err)
	}
	derivedIntentID, err := d.Exploration(task.ExplorationID).AddIntent(
		map[string]any{"summary": "derived service"}, 5, []int64{serviceID}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := d.Exploration(task.ExplorationID).ClaimIntent(derivedIntentID, "worker"); err != nil || !claimed {
		t.Fatalf("derived claim=%v err=%v", claimed, err)
	}
	running, err := assets.RunningIntentIDsForAssets(task.ID, []int64{rootID})
	if err != nil || len(running) != 1 || running[0] != derivedIntentID {
		t.Fatalf("parent authorization change did not select derived Worker: ids=%v err=%v", running, err)
	}
	if err := d.Exploration(task.ExplorationID).SetNodeState(derivedIntentID, "stopped"); err != nil {
		t.Fatal(err)
	}

	intentID, err := d.Exploration(task.ExplorationID).AddIntent(
		map[string]any{"summary": "approval claim"}, 5, []int64{rootID}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := d.Exploration(task.ExplorationID).ClaimIntent(intentID, "worker"); err != nil || !claimed {
		t.Fatalf("approved claim=%v err=%v, want claimed", claimed, err)
	}
	if err := d.Exploration(task.ExplorationID).SetNodeState(intentID, "open"); err != nil {
		t.Fatal(err)
	}

	detached, err := assets.DetachAssetFromTask(task.ID, rootID)
	if err != nil || !detached {
		t.Fatalf("detach=%v err=%v", detached, err)
	}
	if err := assets.ValidateTaskAssetsApproved(task.ID, []int64{rootID}); !errors.Is(err, ErrTaskAssetBlocked) {
		t.Fatalf("tombstoned root validation=%v, want ErrTaskAssetBlocked", err)
	}
	if claimed, err := d.Exploration(task.ExplorationID).ClaimIntent(intentID, "worker"); err != nil || claimed {
		t.Fatalf("tombstoned claim=%v err=%v, want rejected", claimed, err)
	}
	// Rediscovery below a deleted parent must not resurrect any of the
	// service/domain side-effect links created by the upsert itself.
	blockedServiceID, err := assets.UpsertHTTPService(UpsertHTTPServiceReq{
		URL: fmt.Sprintf("https://approval-%d.test/", suffix), TaskID: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := assets.RegisterAgentDiscoveredAsset(task.ID, blockedServiceID, "worker"); !errors.Is(err, ErrTaskAssetBlocked) || state != "blocked" {
		t.Fatalf("blocked service rediscovery state=%q err=%v", state, err)
	}
	var resurrected int
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE $1=ANY(task_ids) AND (domain=$2 OR root_domain=$2)`, task.ID, fmt.Sprintf("approval-%d.test", suffix)).Scan(&resurrected); err != nil {
		t.Fatal(err)
	}
	if resurrected != 0 {
		t.Fatalf("blocked rediscovery resurrected %d host links", resurrected)
	}

	// Reattaching is an explicit user action: it clears only this task's
	// tombstone and restores the task-local approval state.
	if _, err := assets.AttachAssetsToTask(task.ID, []int64{rootID}, "operator reattach"); err != nil {
		t.Fatal(err)
	}
	if err := assets.ValidateTaskAssetsApproved(task.ID, []int64{rootID}); err != nil {
		t.Fatalf("reattached root validation=%v", err)
	}

	other, err := d.CreateTask(fmt.Sprintf("approval-other-%d", suffix), "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	if _, err := assets.AttachAssetsToTask(other.ID, []int64{rootID}, "other task"); err != nil {
		t.Fatal(err)
	}
	if err := assets.RevokeTaskAssets(task.ID, []int64{rootID}, "operator", "withdraw"); err != nil {
		t.Fatal(err)
	}
	if err := assets.ValidateTaskAssetsApproved(task.ID, []int64{rootID}); !errors.Is(err, ErrTaskAssetNotApproved) {
		t.Fatalf("revoked current task validation=%v", err)
	}
	if err := assets.ValidateTaskAssetsApproved(other.ID, []int64{rootID}); err != nil {
		t.Fatalf("other task authorization leaked from revoke: %v", err)
	}
}

func assetListContains(assets []*Asset, id int64) bool {
	for _, asset := range assets {
		if asset.ID == id {
			return true
		}
	}
	return false
}

func TestAgentDiscoverySideEffectsStayPendingAndManualApprovalIsDurable(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	discoveryTask, err := d.CreateTaskWithOptions(fmt.Sprintf("side-effects-%d", suffix), "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	manualTask, err := d.CreateTask(fmt.Sprintf("manual-durable-%d", suffix), "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(manualTask.ID); _ = d.DeleteTask(discoveryTask.ID) })

	ip := fmt.Sprintf("198.51.%d.%d", (suffix/250)%250, suffix%250+1)
	domain := fmt.Sprintf("side-%d.assets.example.com", suffix)
	subdomainID, err := d.Assets().UpsertSubdomain(UpsertSubdomainReq{
		Domain: domain, RecordType: "A", RecordValue: []string{ip}, TaskID: discoveryTask.ID, AgentDiscovered: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var initialState string
	if err := d.QueryRow(`SELECT approval_state FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, discoveryTask.ID, subdomainID).Scan(&initialState); err != nil {
		t.Fatal(err)
	}
	if initialState != ApprovalPending {
		t.Fatalf("initial agent link state=%q, want pending before registration", initialState)
	}
	if state, err := d.Assets().RegisterAgentDiscoveredAsset(discoveryTask.ID, subdomainID, "worker"); err != nil || state != ApprovalPending {
		t.Fatalf("subdomain state=%q err=%v, want pending", state, err)
	}
	rows, err := d.Query(`SELECT a.type,l.approval_state
FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
WHERE l.task_id=$1 AND (a.id=$2 OR a.ip=$3 OR a.domain='example.com')`, discoveryTask.ID, subdomainID, ip)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for rows.Next() {
		var typ, state string
		if err := rows.Scan(&typ, &state); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		states[typ] = state
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"root_domain", "subdomain", "ip"} {
		if states[typ] != ApprovalPending {
			t.Fatalf("side-effect %s state=%q, all states=%v", typ, states[typ], states)
		}
	}

	manualID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("manual-%d.example.net", suffix)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Assets().AttachAssetsToTask(manualTask.ID, []int64{manualID}, "operator selected"); err != nil {
		t.Fatal(err)
	}
	if state, err := d.Assets().RegisterAgentDiscoveredAsset(manualTask.ID, manualID, "worker"); err != nil || state != ApprovalApproved {
		t.Fatalf("manual rediscovery state=%q err=%v, want approved", state, err)
	}
}

func TestAssetKeyIsStableForNonHTTPServiceAndApp(t *testing.T) {
	port := 22
	service := &Asset{Type: "service", IP: "2001:0db8::7", Port: &port, ServiceName: " SSH "}
	if key, host := AssetKey(service); key != "service:2001:db8::7:22:ssh" || host != "2001:db8::7" {
		t.Fatalf("service AssetKey=(%q,%q)", key, host)
	}
	app := &Asset{Type: "app", BundleID: " COM.Example.App ", AppName: "ignored"}
	if key, _ := AssetKey(app); key != "app:com.example.app" {
		t.Fatalf("app AssetKey=%q", key)
	}
}

func TestAgentRediscoveryCanReattachServiceAfterRecordDeletion(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	task, err := d.CreateTask(fmt.Sprintf("recreated tombstone %d", suffix), "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	domain := fmt.Sprintf("recreated-%d.example.test", suffix)
	firstID, err := d.Assets().UpsertOtherService(UpsertOtherServiceReq{Domain: domain, Port: 2222, ServiceName: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(task.ID)
		_, _ = d.Exec(`DELETE FROM assets WHERE type='service' AND domain=$1 AND port=2222`, domain)
	})
	if _, err := d.Assets().AttachAssetsToTask(task.ID, []int64{firstID}, "operator selected"); err != nil {
		t.Fatal(err)
	}
	if detached, err := d.Assets().DetachAssetFromTask(task.ID, firstID); err != nil || !detached {
		t.Fatalf("detach=%v err=%v", detached, err)
	}
	if _, err := d.Assets().DeleteByIDs([]int64{firstID}); err != nil {
		t.Fatal(err)
	}

	recreatedID, err := d.Assets().UpsertOtherService(UpsertOtherServiceReq{
		Domain: domain, Port: 2222, ServiceName: "ssh", TaskID: task.ID, AgentDiscovered: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recreatedID == firstID {
		t.Fatal("global asset was not recreated with a new id")
	}
	state, err := d.Assets().RegisterAgentDiscoveredAsset(task.ID, recreatedID, "worker")
	if err != nil || state != ApprovalApproved {
		t.Fatalf("rediscovery state=%q err=%v", state, err)
	}
	var attached, linked bool
	if err := d.QueryRow(`SELECT $1=ANY(task_ids) FROM assets WHERE id=$2`, task.ID, recreatedID).Scan(&attached); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_asset_links WHERE task_id=$1 AND asset_id=$2)`, task.ID, recreatedID).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if !attached || !linked {
		t.Fatalf("service rediscovery failed to attach: task_ids=%v link=%v", attached, linked)
	}
}

func TestIntentAssetsIncludesDirectSourceProvenance(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	source, err := d.CreateTask("source assets", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("current assets", "goal", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(current.ID); _ = d.DeleteTask(source.ID) })

	assetID, err := d.Assets().UpsertHTTPService(UpsertHTTPServiceReq{
		URL: fmt.Sprintf("https://intent-%d.example.test", time.Now().UnixNano()), TaskID: source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs([]int64{assetID}) })
	intentID, err := d.Exploration(source.ExplorationID).AddIntent(map[string]any{"summary": "source worker"}, 5, []int64{assetID}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Exploration(source.ExplorationID).SetNodeState(intentID, "done"); err != nil {
		t.Fatal(err)
	}
	nodeID := intentID
	if err := d.Assets().SetTaskAssetSource(source.ID, assetID, "agent", "Worker 通过 insert_assets 登记", &nodeID); err != nil {
		t.Fatal(err)
	}

	assets, err := d.Assets().IntentAssets(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].IntentID != intentID || assets[0].SourceTaskID != source.ID || !assets[0].Inherited {
		t.Fatalf("unexpected intent assets: %+v", assets)
	}
	if assets[0].Source != "agent" || assets[0].SourceSummary == "" || assets[0].SourceNodeID == nil || *assets[0].SourceNodeID != intentID {
		t.Fatalf("unexpected intent provenance: %+v", assets[0])
	}
	if err := d.Assets().ValidateTaskAssetsApproved(current.ID, []int64{assetID}); err != nil {
		t.Fatalf("approved source authorization=%v", err)
	}
	taskAssets, err := d.Assets().QueryByTaskApproval(current.ID, "service", "all", "all", 10, 0)
	if err != nil || len(taskAssets) != 1 {
		t.Fatalf("inherited task asset page=%+v err=%v", taskAssets, err)
	}
	if !taskAssets[0].TaskInherited || !taskAssets[0].TaskReadOnly || taskAssets[0].TaskSourceTaskID != source.ID {
		t.Fatalf("inherited task asset metadata=%+v", taskAssets[0])
	}
	approvals, err := d.Assets().ListTaskAssetApprovals(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	var parentID int64
	for i := range approvals {
		if approvals[i].AssetID == assetID {
			t.Fatal("derived asset appeared in approval panel")
		}
		if approvals[i].AssetType == "subdomain" && approvals[i].Inherited && approvals[i].ReadOnly && approvals[i].SourceTaskID == source.ID {
			parentID = approvals[i].AssetID
		}
	}
	if parentID == 0 {
		t.Fatalf("inherited approval rows=%+v err=%v", approvals, err)
	}
	currentIntentID, err := d.Exploration(current.ExplorationID).AddIntent(map[string]any{"summary": "current inherited worker"}, 5, []int64{assetID}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := d.Exploration(current.ExplorationID).ClaimIntent(currentIntentID, "worker"); err != nil || !claimed {
		t.Fatalf("claim inherited intent=%v err=%v", claimed, err)
	}
	running, err := d.Assets().RunningIntentIDsForAssets(current.ID, []int64{parentID})
	if err != nil || len(running) != 1 || running[0] != currentIntentID {
		t.Fatalf("running inherited intents=%v err=%v", running, err)
	}
	if err := d.Exploration(current.ExplorationID).SetNodeState(currentIntentID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := d.Assets().RevokeTaskAssets(source.ID, []int64{parentID}, "operator", "source withdrawal"); err != nil {
		t.Fatal(err)
	}
	if err := d.Assets().ValidateTaskAssetsApproved(current.ID, []int64{assetID}); !errors.Is(err, ErrTaskAssetNotApproved) {
		t.Fatalf("revoked source authorization=%v, want ErrTaskAssetNotApproved", err)
	}
	if err := d.Assets().ApproveTaskAssets(source.ID, []int64{parentID}, "operator", "source restore"); err != nil {
		t.Fatal(err)
	}
	if detached, err := d.Assets().DetachAssetFromTask(current.ID, assetID); err != nil || !detached {
		t.Fatalf("inherited task-local detach=%v err=%v", detached, err)
	}
	if err := d.Assets().ValidateTaskAssetsApproved(current.ID, []int64{assetID}); err != nil {
		t.Fatalf("service record exclusion changed host authorization: %v", err)
	}
	if err := d.Assets().ValidateTaskAssetsApproved(source.ID, []int64{assetID}); err != nil {
		t.Fatalf("current tombstone leaked into source task: %v", err)
	}
	blockedRows, err := d.Assets().QueryByTaskApproval(current.ID, "service", "all", "all", 10, 0)
	if err != nil || len(blockedRows) != 0 {
		t.Fatalf("excluded service still shown: %+v err=%v", blockedRows, err)
	}
	if _, err := d.Assets().AttachAssetsToTask(current.ID, []int64{assetID}, "manual override"); err != nil {
		t.Fatal(err)
	}
	if err := d.Assets().ValidateTaskAssetsApproved(current.ID, []int64{assetID}); err != nil {
		t.Fatalf("manual reattach did not restore current authorization: %v", err)
	}
	restored, err := d.Assets().QueryByTaskApproval(current.ID, "service", "all", "all", 10, 0)
	if err != nil || len(restored) != 1 || restored[0].Blocked || restored[0].TaskInherited || restored[0].TaskReadOnly {
		t.Fatalf("reattached task asset metadata=%+v err=%v", restored, err)
	}
}

func TestIntentAssetsHonorsCanceledReadContext(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTask("canceled intent assets", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = d.Assets().WithReadContext(ctx).IntentAssets(task.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("IntentAssets error=%v, want context.Canceled", err)
	}
}

func TestIntentAssetsPageLimitsIntentsBeforeJoiningAssets(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTask("paged intent assets", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("intent-page-%d.example.test", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(task.ID)
		_, _ = d.Assets().DeleteByIDs([]int64{assetID})
	})
	if _, err := d.Assets().AttachAssetsToTask(task.ID, []int64{assetID}, "test target"); err != nil {
		t.Fatal(err)
	}
	intentIDs := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		intentID, err := d.Exploration(task.ExplorationID).AddIntent(
			map[string]any{"summary": fmt.Sprintf("intent %d", i)}, 1, []int64{assetID}, "planner",
		)
		if err != nil {
			t.Fatal(err)
		}
		intentIDs = append(intentIDs, intentID)
	}
	page, err := d.Assets().IntentAssetsPage(task.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].IntentID != intentIDs[2] || page[1].IntentID != intentIDs[1] {
		t.Fatalf("newest intent asset page=%+v, want intent ids %v", page, intentIDs[1:])
	}
	page, err = d.Assets().IntentAssetsPage(task.ID, intentIDs[1], 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].IntentID != intentIDs[0] {
		t.Fatalf("older intent asset page=%+v, want intent id %d", page, intentIDs[0])
	}
}

func TestInheritedApprovalListSelectsOneEffectiveSource(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	stamp := time.Now().UnixNano()
	pendingSource, err := d.CreateTaskWithOptions(fmt.Sprintf("pending-source-%d", stamp), "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	approvedSource, err := d.CreateTask(fmt.Sprintf("approved-source-%d", stamp), "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions(fmt.Sprintf("multi-source-%d", stamp), "goal", TaskCreateOptions{
		SourceTaskIDs: []int64{pendingSource.ID, approvedSource.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(current.ID)
		_ = d.DeleteTask(approvedSource.ID)
		_ = d.DeleteTask(pendingSource.ID)
	})

	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("multi-source-%d.invalid", stamp), TaskID: pendingSource.ID, AgentDiscovered: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs([]int64{assetID}) })
	if _, err := d.Assets().AttachAssetsToTask(approvedSource.ID, []int64{assetID}, "operator selected"); err != nil {
		t.Fatal(err)
	}

	approvals, err := d.Assets().ListTaskAssetApprovals(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	matching := make([]TaskAssetApproval, 0, 1)
	for _, approval := range approvals {
		if approval.AssetID == assetID {
			matching = append(matching, approval)
		}
	}
	if len(matching) != 1 || matching[0].SourceTaskID != approvedSource.ID || matching[0].ApprovalState != ApprovalApproved {
		t.Fatalf("effective inherited approvals=%+v", matching)
	}
}

func TestTaskCreationDirectAssetsHaveIndependentTestState(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	domain := fmt.Sprintf("direct-task-%d.example.test", time.Now().UnixNano())
	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Assets().DeleteByIDs([]int64{assetID}) })
	first, err := d.CreateTaskWithOptions("direct first", "goal", TaskCreateOptions{AssetIDs: []int64{assetID, assetID}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.CreateTaskWithOptions("direct second", "goal", TaskCreateOptions{AssetIDs: []int64{assetID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(first.ID); _ = d.DeleteTask(second.ID) })

	page, err := d.Assets().QueryByTaskTested(first.ID, "root_domain", "false", 10, 0)
	if err != nil || len(page) != 1 || page[0].Tested == nil || *page[0].Tested || page[0].TaskSource != "direct" {
		t.Fatalf("initial direct task asset=%+v err=%v", page, err)
	}
	if err := d.Assets().MarkTaskAssetsTested(first.ID, []int64{assetID}, "worker"); err != nil {
		t.Fatal(err)
	}
	page, err = d.Assets().QueryByTaskTested(first.ID, "root_domain", "true", 10, 0)
	if err != nil || len(page) != 1 || page[0].Tested == nil || !*page[0].Tested || page[0].TestedBy != "worker" || page[0].TestedAt == nil {
		t.Fatalf("marked direct task asset=%+v err=%v", page, err)
	}
	page, err = d.Assets().QueryByTaskTested(second.ID, "root_domain", "false", 10, 0)
	if err != nil || len(page) != 1 || page[0].Tested == nil || *page[0].Tested {
		t.Fatalf("second task state leaked=%+v err=%v", page, err)
	}
	scopes, err := d.Assets().ListTaskScope(first.ID)
	if err != nil || len(scopes) != 1 || scopes[0].Kind != "root_domain" || scopes[0].Domain != domain {
		t.Fatalf("direct asset scope=%+v err=%v", scopes, err)
	}
}

func TestTaskCreationMissingAssetRollsBack(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()
	description := fmt.Sprintf("missing-direct-asset-%d", time.Now().UnixNano())
	if _, err := d.CreateTaskWithOptions(description, "goal", TaskCreateOptions{AssetIDs: []int64{1 << 62}}); !errors.Is(err, ErrTaskAssetAssetNotFound) {
		t.Fatalf("missing asset error=%v", err)
	}
	var tasks, explorations int
	if err := d.QueryRow(`SELECT count(*) FROM tasks WHERE description=$1`, description).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM explorations WHERE description=$1`, description).Scan(&explorations); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 || explorations != 0 {
		t.Fatalf("failed create leaked rows: tasks=%d explorations=%d", tasks, explorations)
	}
}
