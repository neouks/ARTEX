package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestTaskFindingsPageHTTPProvenanceAndLegacy(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	create := func() *Task {
		t.Helper()
		task, e := m.CreateTask("finding page", "", nil, 0, 0)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			m.pg.Exec(`DELETE FROM findings WHERE task_id=$1`, mustTaskID(t, task.ID))
			m.pg.DeleteTask(mustTaskID(t, task.ID))
		})
		return task
	}
	source, owner, other := create(), create(), create()
	if _, err = m.pg.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, owner.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	for _, task := range []*Task{source, owner, other} {
		if _, err = m.pg.AddFinding(mustTaskID(t, task.ID), 0, "test", "title", "high", "summary", "full evidence", "test", nil); err != nil {
			t.Fatal(err)
		}
	}
	var nodeID int64
	if err = m.pg.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,state,payload) SELECT exploration_id,'finding','confirmed','{"summary":"legacy","evidence":"证据"}'::jsonb FROM tasks WHERE id=$1 RETURNING id`, source.ID).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m}
	call := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.findings(w, httptest.NewRequest("GET", "/api/exploration/findings?"+query, nil))
		return w
	}
	for _, q := range []string{"context_task=bad", "context_task=0", "context_task=" + owner.ID + "&page=0", "context_task=" + owner.ID + "&limit=201", "context_task=" + owner.ID + "&direction=wrong", "context_task=" + owner.ID + "&legacy_node=x"} {
		if w := call(q); w.Code != 400 {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
	}
	w := call("context_task=" + owner.ID + "&page=1&limit=20")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var result struct {
		Items []FindingDTO `json:"items"`
		Total int          `json:"total"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || len(result.Items) != 3 {
		t.Fatalf("%s", w.Body)
	}
	legacyCount := 0
	for _, item := range result.Items {
		if item.TaskID == source.ID && (!item.Inherited || item.SourceTaskID != source.ID) {
			t.Fatalf("missing provenance %+v", item)
		}
		if item.TaskID == owner.ID && item.Inherited {
			t.Fatal("own row read only")
		}
		if item.FindingID == "" {
			legacyCount++
			if item.ID != fmt.Sprint(nodeID) {
				t.Fatal("legacy identity")
			}
		}
		if item.Evidence != "" {
			t.Fatal("list preloaded evidence")
		}
	}
	if legacyCount != 1 {
		t.Fatal("lost legacy")
	}
	w = call(fmt.Sprintf("context_task=%s&legacy_node=%d", owner.ID, nodeID))
	var detail FindingDTO
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Evidence != "证据" || detail.FindingID != "" || !detail.Inherited {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w = call(fmt.Sprintf("context_task=%s&legacy_node=%d", other.ID, nodeID)); w.Code != 404 {
		t.Fatalf("legacy scope leak %s", w.Body)
	}
	w = call("task_id=" + owner.ID + "&page=1&limit=20")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Total != 1 {
		t.Fatalf("global behavior changed %s", w.Body)
	}
	if result.Items[0].Evidence != "full evidence" {
		t.Fatal("legacy global projection changed")
	}
	w = call("task_id=" + owner.ID + "&page=1&limit=20&summary_only=1")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Total != 1 || result.Items[0].Evidence != "" {
		t.Fatalf("summary projection %s", w.Body)
	}
}
