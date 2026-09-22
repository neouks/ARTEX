package server

import (
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/db"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestActivitySearchValidationAndIsolation(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("search test", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	id := fmt.Sprint(task.ID)
	es := d.Exploration(task.ExplorationID)
	for range 3 {
		if _, err := es.AppendActivity(db.Activity{Worker: "planner", Kind: "text", Detail: "中文 NEEDLE %_"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{m: &Manager{tasks: map[string]*Task{id: {ID: id, Store: es}}}}
	request := func(session, q, extra string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/exploration/activity/search?task="+id+"&session="+session+"&q="+url.QueryEscape(q)+extra, nil)
		w := httptest.NewRecorder()
		s.activitySearch(w, r)
		return w
	}
	for _, tc := range []struct {
		session, q, extra string
		status            int
	}{{"plan", "中文", "", 200}, {"main:0", "needle", "", 200}, {"plan", "", "", 400}, {"plan", strings.Repeat("中", 201), "", 400}, {"plan", "needle", "&limit=51", 400}, {"bad", "x", "", 400}, {"plan", "x", "&cursor=invalid", 400}} {
		w := request(tc.session, tc.q, tc.extra)
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := request("plan", "needle", "&limit=1")
	var p struct {
		Items []db.ActivitySearchHit `json:"items"`
		Next  string                 `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || len(p.Items) != 1 || p.Next == "" {
		t.Fatal(w.Body.String(), err)
	}
	if w = request("main:0", "needle", "&cursor="+p.Next); w.Code != 400 {
		t.Fatal("cross session cursor", w.Body.String())
	}
	if w = request("plan", "needle", "&cursor="+p.Next); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = request("main:0", "needle", ""); !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("leaked other session", w.Body.String())
	}
}

func TestActivitySearchInheritedAndUnrelatedTask(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	source, err := d.CreateTask("search source", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(source.ID)
	current, err := d.CreateTaskWithOptions("search inherited", "goal", db.TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(current.ID)
	other, err := d.CreateTask("search unrelated", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	sourceStore := d.Exploration(source.ExplorationID)
	node, err := sourceStore.AddIntent(map[string]any{"summary": "test"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	a, err := sourceStore.AppendActivity(db.Activity{Worker: "work#1", NodeID: &node, Kind: "text", Detail: "shared body"})
	if err != nil {
		t.Fatal(err)
	}
	if err = sourceStore.SetIntentState(node, "done"); err != nil {
		t.Fatal(err)
	}
	tasks := map[string]*Task{}
	for _, v := range []struct{ id, exp int64 }{{source.ID, source.ExplorationID}, {current.ID, current.ExplorationID}, {other.ID, other.ExplorationID}} {
		id := fmt.Sprint(v.id)
		tasks[id] = &Task{ID: id, Store: d.Exploration(v.exp)}
	}
	s := &Server{m: &Manager{tasks: tasks}}
	for _, tc := range []struct {
		id     int64
		status int
	}{{current.ID, 200}, {other.ID, 404}} {
		q := fmt.Sprintf("?task=%d&session=intent:%d", tc.id, node)
		for _, history := range []bool{false, true} {
			w := httptest.NewRecorder()
			if history {
				s.activityHistory(w, httptest.NewRequest("GET", "/api/exploration/activity/history"+q+fmt.Sprintf("&around=%d", a), nil))
			} else {
				s.activitySearch(w, httptest.NewRequest("GET", "/api/exploration/activity/search"+q+"&q=shared", nil))
			}
			if w.Code != tc.status {
				t.Fatalf("task %d history %v: %d %s", tc.id, history, w.Code, w.Body.String())
			}
		}
	}
}
