package db

import (
	"context"
	"testing"
	"time"
)

func TestActivitySearchFullBodySnapshotAndScope(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("test", "search")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	s := d.Exploration(exp)
	seg := 2
	add := func(a Activity) int64 {
		t.Helper()
		id, e := s.AppendActivity(a)
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	first := add(Activity{Worker: "mainagent", Kind: "user", Summary: "无命中摘要", Detail: "正文 中文 KeY %_[]'\\ <script>"})
	result := add(Activity{Worker: "mainagent", Kind: "result", Summary: "中文 key 摘要回退"})
	add(Activity{Worker: "mainagent", MainSeg: &seg, Kind: "text", Detail: "中文 key"})
	add(Activity{Worker: "planner", Kind: "text", Detail: "中文 key"})
	f := ActivitySessionFilter{Main: true}
	hits, more, upper, e := s.ActivitySearch(context.Background(), f, "KEY", 0, 0, 1)
	if e != nil || !more || len(hits) != 1 || hits[0].ID != first {
		t.Fatalf("%+v %v %v", hits, more, e)
	}
	add(Activity{Worker: "mainagent", Kind: "text", Detail: "中文 key 新消息"})
	hits, more, _, e = s.ActivitySearch(context.Background(), f, "KEY", first, upper, 1)
	if e != nil || more || len(hits) != 1 || hits[0].ID != result {
		t.Fatalf("snapshot %+v %v %v", hits, more, e)
	}
	for _, q := range []string{"中文", "%_[]'\\", "<script>"} {
		hits, _, _, e = s.ActivitySearch(context.Background(), f, q, 0, upper, 50)
		if e != nil || len(hits) == 0 {
			t.Fatalf("literal %s: %v", q, e)
		}
	}
	hits, _, _, e = s.ActivitySearch(context.Background(), f, "不存在", 0, 0, 20)
	if e != nil || len(hits) != 0 {
		t.Fatal(hits, e)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, _, _, e = s.ActivitySearch(ctx, f, "中文", 0, 0, 20); e == nil {
		t.Fatal("cancelled search succeeded")
	}
	for _, id := range []int64{first, result} {
		p, e := s.ActivityWindow(f, id, 0, 2)
		if e != nil || len(p.Items) == 0 {
			t.Fatal("ordinary anchor", e)
		}
	}
}

func TestActivityWindowResultAnchor(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("test", "result anchor")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	s := d.Exploration(exp)
	add := func(kind, call string) int64 {
		id, e := s.AppendActivity(Activity{Worker: "planner", Kind: kind, ToolUseID: call})
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	add("tool_use", "same")
	add("tool_result", "same")
	use := add("tool_use", "same")
	for range 20 {
		add("text", "")
	}
	result := add("tool_result", "same")
	p, e := s.ActivityWindow(ActivitySessionFilter{Worker: "planner"}, result, 0, 2)
	if e != nil {
		t.Fatal(e)
	}
	if p.Items[0].ID != use || p.Items[len(p.Items)-1].ID != result || len(p.Items) != 22 {
		t.Fatalf("wrong interval %+v", p)
	}
}
