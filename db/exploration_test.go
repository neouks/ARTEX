package db

import (
	"fmt"
	"testing"
)

func TestExplorationFlow(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	expID, err := d.CreateExploration("test", "拿下测试目标")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) // cascades nodes/edges/activity
	es := d.Exploration(expID)

	// goal node + two intents
	goal, err := es.AddGoal(map[string]any{"text": "getadmin", "vulnclass": "authz"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := es.AddIntent(map[string]any{"summary": "enumerate endpoints"}, 5, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	i2, err := es.AddIntent(map[string]any{"summary": "test idor"}, 8, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	// frontier ordered by priority desc → i2(8) before i1(5)
	fr, err := es.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) != 2 || fr[0].ID != i2 {
		t.Fatalf("frontier order wrong: %+v", fr)
	}

	// atomic claim: first wins, second on same id fails
	ok, err := es.ClaimIntent(i2, "worker-1")
	if err != nil || !ok {
		t.Fatalf("claim i2: ok=%v err=%v", ok, err)
	}
	ok2, _ := es.ClaimIntent(i2, "worker-2")
	if ok2 {
		t.Fatalf("double-claim should fail")
	}

	// finding yields from intent, proves goal
	find, err := es.AddNode("finding", map[string]any{"vulnclass": "idor", "severity": "high"}, 9, "confirmed", "worker-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := es.Link(i2, "yields", find); err != nil {
		t.Fatal(err)
	}
	if err := es.Link(find, "proves", goal); err != nil {
		t.Fatal(err)
	}
	if err := es.SetNodeState(goal, "met"); err != nil {
		t.Fatal(err)
	}

	// lineage: ancestors of the finding traced backward — here {i2, find} joined by
	// the yields edge. The proves→goal edge is DOWNSTREAM (goal must be excluded),
	// and the unrelated intent i1 is not on any path to the finding (excluded too).
	lnNodes, lnEdges, err := es.FindingLineage(find)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, n := range lnNodes {
		got[n.ID] = true
	}
	if len(lnNodes) != 2 || !got[find] || !got[i2] {
		t.Fatalf("lineage nodes: want {i2,find}, got %+v", lnNodes)
	}
	if got[goal] {
		t.Fatalf("lineage must exclude the proved goal (it is downstream of the finding)")
	}
	if len(lnEdges) != 1 || lnEdges[0].From != i2 || lnEdges[0].To != find || lnEdges[0].Rel != "yields" {
		t.Fatalf("lineage edges: want i2-yields->find, got %+v", lnEdges)
	}

	// activity poll by id cursor
	id1, err := es.AppendActivity(Activity{Worker: "worker-1", Kind: "tool_use", Tool: "Bash", Summary: "ran curl", Detail: "full output"})
	if err != nil {
		t.Fatal(err)
	}
	items, cursor, err := es.ActivityList(nil, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || cursor != id1 {
		t.Fatalf("activity list: items=%d cursor=%d", len(items), cursor)
	}
	det, _ := es.ActivityDetail(id1)
	if det != "full output" {
		t.Fatalf("detail want 'full output', got %q", det)
	}
	// incremental: nothing new after cursor
	items2, _, _ := es.ActivityList(nil, cursor, 100)
	if len(items2) != 0 {
		t.Fatalf("incremental poll should be empty, got %d", len(items2))
	}

	// stats
	st, _ := es.Stats()
	if st["intent"] != 2 || st["goal"] != 1 || st["finding"] != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestIntentFingerprintNormalizesSummaryAndAssets(t *testing.T) {
	first := IntentFingerprint("  Test   IDOR\nOn API  ", []int64{7, 3, 7})
	second := IntentFingerprint("test idor on api", []int64{3, 7})
	if first == "" || first != second {
		t.Fatalf("equivalent intents must share a fingerprint: %q != %q", first, second)
	}
	if first == IntentFingerprint("test idor on api", []int64{3, 8}) {
		t.Fatal("different target assets must not share a fingerprint")
	}
}

func TestAddIntentDeduplicatesOnlyActiveFrontier(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("dedup", "dedup active intents")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	store := d.Exploration(expID)

	first, created, err := store.AddIntentDeduplicated(map[string]any{"summary": " Test   IDOR "}, 5, nil, "planner")
	if err != nil || !created {
		t.Fatalf("first admission: id=%d created=%v err=%v", first, created, err)
	}
	duplicate, created, err := store.AddIntentDeduplicated(map[string]any{"summary": "test idor"}, 9, nil, "human")
	if err != nil || created || duplicate != first {
		t.Fatalf("active duplicate: id=%d created=%v err=%v; want existing %d", duplicate, created, err, first)
	}
	if err := store.SetIntentState(first, "done"); err != nil {
		t.Fatal(err)
	}
	retry, created, err := store.AddIntentDeduplicated(map[string]any{"summary": "test idor"}, 9, nil, "human")
	if err != nil || !created || retry == first {
		t.Fatalf("terminal retry: id=%d created=%v err=%v", retry, created, err)
	}
}

func TestBlackboardRevisionChangesWithGraph(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("revision", "watch graph")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	store := d.Exploration(expID)
	before, err := store.BlackboardRevision()
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := store.AddIntent(map[string]any{"summary": "new route"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	afterInsert, err := store.BlackboardRevision()
	if err != nil || afterInsert == before {
		t.Fatalf("revision did not change after insert: before=%q after=%q err=%v", before, afterInsert, err)
	}
	if err := store.SetIntentState(intentID, "running"); err != nil {
		t.Fatal(err)
	}
	afterState, err := store.BlackboardRevision()
	if err != nil || afterState == afterInsert {
		t.Fatalf("revision did not change after state transition: insert=%q state=%q err=%v", afterInsert, afterState, err)
	}
}

func TestBlackboardRevisionTracksTaskAssetContext(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	source, err := d.CreateTask("revision source", "source goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("revision current", "current goal", TaskCreateOptions{
		SourceTaskIDs: []int64{source.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = d.DeleteTask(current.ID)
		_ = d.DeleteTask(source.ID)
	}()
	store := d.Exploration(current.ExplorationID)
	revision := func() string {
		t.Helper()
		value, err := store.BlackboardRevision()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	wantChanged := func(label, before string) string {
		t.Helper()
		after := revision()
		if after == before {
			t.Fatalf("revision did not change after %s: %q", label, after)
		}
		return after
	}

	assetIDs := make([]int64, 0, 4)
	defer func() { _, _ = d.Assets().DeleteByIDs(assetIDs) }()

	before := revision()
	unrelatedID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("revision-unrelated-%d.invalid", current.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	assetIDs = append(assetIDs, unrelatedID)
	if after := revision(); after != before {
		t.Fatalf("unrelated asset insertion changed revision: before=%q after=%q", before, after)
	}
	if _, err := d.Exec(`UPDATE assets SET extra=extra || '{"unrelated":true}'::jsonb WHERE id=$1`, unrelatedID); err != nil {
		t.Fatal(err)
	}
	if after := revision(); after != before {
		t.Fatalf("unrelated asset update changed revision: before=%q after=%q", before, after)
	}

	directID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("revision-direct-%d.invalid", current.ID), TaskID: current.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assetIDs = append(assetIDs, directID)
	before = wantChanged("task asset insertion", before)
	if _, err := d.Exec(`UPDATE assets SET extra=extra || '{"enriched":true}'::jsonb WHERE id=$1`, directID); err != nil {
		t.Fatal(err)
	}
	before = wantChanged("task asset enrichment", before)
	if _, err := d.Exec(`UPDATE task_asset_links SET source_summary='revision provenance update' WHERE task_id=$1 AND asset_id=$2`, current.ID, directID); err != nil {
		t.Fatal(err)
	}
	before = wantChanged("task asset provenance update", before)

	sourceID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("revision-source-%d.invalid", source.ID), TaskID: source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assetIDs = append(assetIDs, sourceID)
	before = wantChanged("direct source task asset insertion", before)

	var taskScopeID int64
	if err := d.QueryRow(`
INSERT INTO task_scope(task_id, kind, value, source, reason)
VALUES ($1, 'keyword', $2, 'manual', 'revision scope') RETURNING id`,
		current.ID, fmt.Sprintf("revision-keyword-%d", current.ID)).Scan(&taskScopeID); err != nil {
		t.Fatal(err)
	}
	before = wantChanged("task scope insertion", before)
	if _, err := d.Exec(`UPDATE task_scope SET reason='revision scope updated' WHERE id=$1`, taskScopeID); err != nil {
		t.Fatal(err)
	}
	before = wantChanged("task scope update", before)

	companyName := fmt.Sprintf("Revision Company %d", current.ID)
	companyID, _, err := d.Companies().UpsertCompany(companyName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, companyID)
	if _, err := d.Exec(`
INSERT INTO task_scope(task_id, kind, company_id, source, reason)
VALUES ($1, 'company', $2, 'manual', 'revision company')`, current.ID, companyID); err != nil {
		t.Fatal(err)
	}
	before = wantChanged("company task scope insertion", before)
	if _, _, err := d.Companies().UpsertCompany(companyName, "https://revision.invalid/logo.png"); err != nil {
		t.Fatal(err)
	}
	before = wantChanged("referenced company update", before)
	added, _, invalid, validationErrors, err := d.Companies().AddScopeInputsChecked(companyID, []ScopeInput{{
		Kind: "keyword", Value: fmt.Sprintf("revision company scope %d", current.ID),
	}}, "revision test")
	if err != nil || added != 1 || invalid != 0 || len(validationErrors) != 0 {
		t.Fatalf("add company scope: added=%d invalid=%d validation=%v err=%v", added, invalid, validationErrors, err)
	}
	before = wantChanged("referenced company scope insertion", before)

	scopedDomain := fmt.Sprintf("revision-scoped-%d.invalid", current.ID)
	if _, err := d.Exec(`
INSERT INTO task_scope(task_id, kind, domain, source, reason)
VALUES ($1, 'root_domain', $2, 'manual', 'revision asset scope')`, current.ID, scopedDomain); err != nil {
		t.Fatal(err)
	}
	before = wantChanged("asset scope insertion", before)
	scopedID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: scopedDomain})
	if err != nil {
		t.Fatal(err)
	}
	assetIDs = append(assetIDs, scopedID)
	if after, err := store.BlackboardRevision(); err != nil || after != before {
		t.Fatalf("unassociated asset changed executable revision: %q err=%v", after, err)
	}
	if _, err := d.Assets().AttachAssetsToTask(current.ID, []int64{scopedID}, "user linked scoped asset"); err != nil {
		t.Fatal(err)
	}
	wantChanged("explicit scope asset association", before)
}

func TestIntentPauseResumeAndCancelCleanup(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	expID, err := d.CreateExploration("test", "worker control cleanup")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("cancel-intent-%d.invalid", expID),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, assetID)

	otherIntent, err := es.AddIntent(map[string]any{"summary": "keep intent"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := es.AddIntent(map[string]any{"summary": "cancel intent"}, 10, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := es.ClaimIntent(intentID, "worker-control-test"); err != nil || !claimed {
		t.Fatalf("initial claim: claimed=%v err=%v", claimed, err)
	}
	if err := es.SetIntentState(intentID, "paused"); err != nil {
		t.Fatal(err)
	}

	frontier, err := es.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range frontier {
		if node.ID == intentID {
			t.Fatalf("paused intent %d must not enter frontier", intentID)
		}
	}
	if claimed, err := es.ClaimIntent(intentID, "worker-while-paused"); err != nil || claimed {
		t.Fatalf("paused intent claim: claimed=%v err=%v", claimed, err)
	}
	if err := es.SetIntentState(intentID, "open"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := es.ClaimIntent(intentID, "worker-after-resume"); err != nil || !claimed {
		t.Fatalf("resumed intent claim: claimed=%v err=%v", claimed, err)
	}
	if err := es.SetIntentState(intentID, "paused"); err != nil {
		t.Fatal(err)
	}

	directFact, err := es.AddNode("fact", map[string]any{"summary": "remove fact"}, 0, "confirmed", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	directFinding, err := es.AddNode("finding", map[string]any{"summary": "remove finding"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	keptFact, err := es.AddNode("fact", map[string]any{"summary": "keep fact"}, 0, "confirmed", "other-worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := es.Link(intentID, RelYields, directFact); err != nil {
		t.Fatal(err)
	}
	if err := es.Link(intentID, RelYields, directFinding); err != nil {
		t.Fatal(err)
	}
	findingRowID, err := es.AddStandaloneFinding(0, directFinding, "test", "cancelled finding", SeverityHigh, "summary", "evidence", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	activityID, err := es.AppendActivity(Activity{NodeID: &intentID, Worker: "worker", Kind: "result", Summary: "remove activity"})
	if err != nil {
		t.Fatal(err)
	}
	keptActivityID, err := es.AppendActivity(Activity{NodeID: &otherIntent, Worker: "other-worker", Kind: "result", Summary: "keep activity"})
	if err != nil {
		t.Fatal(err)
	}

	cleanup, err := es.CancelIntent(intentID)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Intents != 1 || cleanup.Facts != 1 || cleanup.Findings != 1 || cleanup.Activities != 1 {
		t.Fatalf("unexpected cleanup counts: %+v", cleanup)
	}
	for _, nodeID := range []int64{intentID, directFact, directFinding} {
		node, err := es.GetNode(nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if node != nil {
			t.Fatalf("node %d survived intent cancellation", nodeID)
		}
	}
	for _, nodeID := range []int64{otherIntent, keptFact} {
		node, err := es.GetNode(nodeID)
		if err != nil || node == nil {
			t.Fatalf("unrelated node %d removed: node=%v err=%v", nodeID, node, err)
		}
	}

	assertCount := func(query string, want int, args ...any) {
		t.Helper()
		var got int
		if err := d.QueryRow(query, args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("query count=%d, want %d: %s", got, want, query)
		}
	}
	assertCount(`SELECT COUNT(*) FROM findings WHERE id=$1`, 0, findingRowID)
	assertCount(`SELECT COUNT(*) FROM activity WHERE id=$1`, 0, activityID)
	assertCount(`SELECT COUNT(*) FROM activity WHERE id=$1`, 1, keptActivityID)
	assertCount(`SELECT COUNT(*) FROM exploration_edges WHERE exploration_id=$1 AND (src_id=$2 OR dst_id=$2)`, 0, expID, intentID)
	assertCount(`SELECT COUNT(*) FROM exploration_anchors WHERE node_id=$1`, 0, directFact)
	assertCount(`SELECT COUNT(*) FROM assets WHERE id=$1`, 1, assetID)
}
