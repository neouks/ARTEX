package server

import (
	"fmt"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestActivityHistoryAnchorScope(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("history anchor", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	other, err := d.CreateTask("other", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	es := d.Exploration(task.ExplorationID)
	aid, err := es.AppendActivity(db.Activity{Worker: "planner", Kind: "tool_use", ToolUseID: "test", Tool: "insert_assets"})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(task.ID, 10)
	oid := strconv.FormatInt(other.ID, 10)
	s := &Server{m: &Manager{tasks: map[string]*Task{id: {ID: id, Store: es}, oid: {ID: oid, Store: d.Exploration(other.ExplorationID)}}}}
	for _, tc := range []struct {
		task, session, query string
		status               int
	}{
		{id, "plan", fmt.Sprintf("around=%d", aid), 200},
		{id, "main:0", fmt.Sprintf("around=%d", aid), 404},
		{oid, "plan", fmt.Sprintf("around=%d", aid), 404},
		{id, "plan", "around=999999999", 404},
		{id, "plan", "around=-1", 400},
		{id, "plan", fmt.Sprintf("around=%d&after=%d", aid, aid), 400},
		{id, "plan", fmt.Sprintf("after=%d", aid), 200},
	} {
		t.Run(tc.task+tc.session+tc.query, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/exploration/activity/history?task="+tc.task+"&session="+tc.session+"&"+tc.query, nil)
			w := httptest.NewRecorder()
			s.activityHistory(w, r)
			if w.Code != tc.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
