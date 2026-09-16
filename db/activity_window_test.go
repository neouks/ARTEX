package db

import (
	"errors"
	"testing"
)

func TestActivityWindowPairAndIsolation(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("test", "window")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	s := d.Exploration(exp)
	f := ActivitySessionFilter{Worker: "planner"}
	add := func(kind, call, worker string) int64 {
		t.Helper()
		id, e := s.AppendActivity(Activity{Kind: kind, ToolUseID: call, Worker: worker, Summary: "step"})
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	for range 10 {
		add("text", "", "planner")
	}
	anchor := add("tool_use", "same", "planner")
	for range 20 {
		add("text", "", "planner")
		add("text", "", "mainagent")
	}
	result := add("tool_result", "same", "planner")
	for range 10 {
		add("text", "", "planner")
	}
	later := add("tool_use", "same", "planner")
	add("tool_result", "same", "planner")
	p, err := s.ActivityWindow(f, anchor, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasOlder || !p.HasNewer {
		t.Fatalf("cursors %+v", p)
	}
	found := false
	for _, a := range p.Items {
		if a.Worker != "planner" {
			t.Fatal("cross session row")
		}
		if a.ID == result {
			found = true
		}
		if a.ID >= later {
			t.Fatal("paired repeated invocation")
		}
	}
	if !found || len(p.Items) != 26 {
		t.Fatalf("missing contiguous pair len=%d found=%v", len(p.Items), found)
	}
	older, more, e := s.ActivityPage(f, p.Items[0].ID, 100)
	if e != nil || more || len(older) != 8 {
		t.Fatalf("older %d %v %v", len(older), more, e)
	}
	next, e := s.ActivityWindow(f, 0, p.Items[len(p.Items)-1].ID, 100)
	if e != nil || next.HasNewer || len(next.Items) != 10 {
		t.Fatalf("next %+v %v", next, e)
	}
	if _, e = s.ActivityWindow(ActivitySessionFilter{Main: true}, anchor, 0, 6); !errors.Is(e, ErrActivityAnchor) {
		t.Fatalf("cross session anchor %v", e)
	}
	if _, e = s.ActivityWindow(f, 999999999, 0, 6); !errors.Is(e, ErrActivityAnchor) {
		t.Fatalf("unknown %v", e)
	}
	if _, e = s.ActivityWindow(f, result, 0, 6); !errors.Is(e, ErrActivityAnchor) {
		t.Fatalf("non command anchor %v", e)
	}
	pending := add("tool_use", "waiting", "planner")
	p, e = s.ActivityWindow(f, pending, 0, 6)
	if e != nil || p.HasNewer {
		t.Fatalf("pending %+v %v", p, e)
	}
	done := add("tool_result", "waiting", "planner")
	p, e = s.ActivityWindow(f, pending, 0, 6)
	if e != nil || p.Items[len(p.Items)-1].ID != done {
		t.Fatalf("completed %+v %v", p, e)
	}
}
