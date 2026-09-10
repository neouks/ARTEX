package db

import "testing"

func TestFindingPatchAtomicProjection(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("patch regression", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	store := d.Exploration(task.ExplorationID)
	node, err := store.AddNode(KindFinding, map[string]any{"severity": "high", "summary": "keep"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.AddFinding(task.ID, node, "XSS", "before", "high", "summary", "evidence", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteFinding(id)
	status, severity, name := "resolved", "invalid", "after"
	if _, err := d.PatchFinding(id, FindingPatch{Status: &status, Severity: &severity}); err == nil {
		t.Fatal("invalid mixed patch succeeded")
	}
	f, err := d.GetFinding(id)
	if err != nil || f.Status != "pending" {
		t.Fatalf("invalid patch changed status: %+v %v", f, err)
	}
	versions, err := store.ContentVersions()
	if err != nil {
		t.Fatal(err)
	}
	severity = "low"
	p := FindingPatch{Status: &status, Severity: &severity, Name: &name}
	if n, err := d.PatchFinding(id, p); err != nil || n != 1 {
		t.Fatalf("patch: %d %v", n, err)
	}
	var gotSeverity, gotName, summary, state string
	var version int
	if err := d.QueryRow(`SELECT payload->>'severity',payload->>'name',payload->>'summary',state,content_version FROM exploration_nodes WHERE id=$1`, node).Scan(&gotSeverity, &gotName, &summary, &state, &version); err != nil {
		t.Fatal(err)
	}
	if gotSeverity != severity || gotName != name || summary != "keep" || state != "confirmed" || version != versions[node]+1 {
		t.Fatalf("projection inconsistent: %s %s %s %s %d", gotSeverity, gotName, summary, state, version)
	}
	if _, err := d.PatchFinding(id, p); err != nil {
		t.Fatal(err)
	}
	newVersions, err := store.ContentVersions()
	if err != nil || newVersions[node] != version {
		t.Fatalf("no-op invalidated digest: %v %v", newVersions, err)
	}
	// Make only the graph update fail; the standalone-row update must roll back.
	if _, err := d.Exec(`CREATE FUNCTION test_patch_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rejection'; END $$`); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DROP FUNCTION test_patch_reject() CASCADE`)
	if _, err := d.Exec(`CREATE TRIGGER test_patch_reject BEFORE UPDATE ON exploration_nodes FOR EACH ROW EXECUTE FUNCTION test_patch_reject()`); err != nil {
		t.Fatal(err)
	}
	name, status = "must rollback", "ignored"
	if _, err := d.PatchFinding(id, p); err == nil {
		t.Fatal("graph failure was ignored")
	}
	f, err = d.GetFinding(id)
	if err != nil || f.Name != "after" || f.Status != "resolved" {
		t.Fatalf("partial patch persisted: %+v %v", f, err)
	}
}
