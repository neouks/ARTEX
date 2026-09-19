package server

import (
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/db"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBroadcastDetailTaskScope(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	a, err := d.CreateTask("broadcast", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(a.ID)
	b, err := d.CreateTask("other", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(b.ID)
	es := d.Exploration(a.ExplorationID)
	id, err := es.AddNode("fact", map[string]any{"summary": "visible", "proof": strings.Repeat("secret", 10000)}, 1, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	aid, bid := fmt.Sprint(a.ID), fmt.Sprint(b.ID)
	s := &Server{m: &Manager{tasks: map[string]*Task{aid: {ID: aid, Store: es}, bid: {ID: bid, Store: d.Exploration(b.ExplorationID)}}}}
	for _, tc := range []struct {
		task, extra string
		want        int
	}{{aid, "", 200}, {bid, "", 404}, {"999999", "", 404}, {aid, "&body_offset=-2", 400}, {aid, "&edge_offset=bad", 400}} {
		r := httptest.NewRequest("GET", "/api/exploration/nodes/x?task="+tc.task+tc.extra, nil)
		r.SetPathValue("id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		s.explorationNodeDetail(w, r)
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("GET", "/api/exploration/nodes?task="+aid, nil)
	w := httptest.NewRecorder()
	s.explorationNodes(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("unbounded list: %s", w.Body)
	}
	var page struct {
		Items []TaskNodeDTO
		Total int
	}
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total == 0 {
		t.Fatal("bad list")
	}
}
