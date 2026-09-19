package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBroadcastBoundedPages(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("test", "broadcast bounded")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	es := d.Exploration(exp)
	raw := strings.Repeat("中", 40000)
	root, err := es.AddNode("fact", map[string]any{"summary": "hub", "evidence": raw}, 1, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Exec(`WITH ns AS (INSERT INTO exploration_nodes(exploration_id,kind,state,payload) SELECT $1,'fact','confirmed',jsonb_build_object('summary','child-'||i,'body',repeat('x',40000)) FROM generate_series(1,125)i RETURNING id) INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) SELECT $1,$2,'yields',id FROM ns`, exp, root)
	if err != nil {
		t.Fatal(err)
	}
	nodes, total, err := es.NodesPageContext(t.Context(), NodeFilter{Query: fmt.Sprint(root)}, 1, 20, false)
	if err != nil || total != 1 || len(nodes) != 1 {
		t.Fatalf("page: %v %d", err, total)
	}
	if len(nodes[0].Payload) > 1000 || strings.Contains(string(nodes[0].Payload), "evidence") {
		t.Fatal("list leaked body")
	}
	detail, err := es.BroadcastNode(t.Context(), root, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(detail.Payload)) != BroadcastBodyPage || len(detail.Edges) != 50 || len(detail.Refs) > 51 || detail.EdgesNext != 50 {
		t.Fatalf("unbounded: %+v", detail)
	}
	payload := detail.Payload
	seen := map[int64]bool{}
	for _, e := range detail.Edges {
		seen[e.To] = true
	}
	for offset := detail.PayloadNext; offset >= 0; {
		p, err := es.BroadcastNode(t.Context(), root, offset, -1)
		if err != nil {
			t.Fatal(err)
		}
		payload += p.Payload
		offset = p.PayloadNext
	}
	var obj map[string]any
	if json.Unmarshal([]byte(payload), &obj) != nil || obj["evidence"] != raw {
		t.Fatal("body pagination lost data")
	}
	for offset := detail.EdgesNext; offset >= 0; {
		p, err := es.BroadcastNode(t.Context(), root, -1, offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Edges {
			if seen[e.To] {
				t.Fatal("duplicate edge")
			}
			seen[e.To] = true
		}
		offset = p.EdgesNext
	}
	if len(seen) != 125 {
		t.Fatalf("lost relations: %d", len(seen))
	}
	count, _, err := es.NodesPageContext(t.Context(), NodeFilter{}, 1, 20, true)
	if err != nil || len(count) != 0 {
		t.Fatal("count fetched nodes")
	}
	other, err := d.CreateExploration("test", "isolated")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, other)
	if v, err := d.Exploration(other).BroadcastNode(t.Context(), root, 0, 0); err != nil || v != nil {
		t.Fatal("cross-task node visible")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err = es.NodesPageContext(ctx, NodeFilter{}, 1, 20, false); err == nil {
		t.Fatal("query ignored cancellation")
	}
	n, err := es.GetNode(root)
	if err != nil || !strings.Contains(string(n.Payload), raw) {
		t.Fatal("audit body changed")
	}
}
