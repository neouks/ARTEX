package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func toolCallsFixture(t *testing.T) (*DB, ToolCallScope, func(Activity) int64) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, err := d.CreateConversation("mainagent", "tool call test", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteConversation(c.ID) })
	return d, ToolCallScope{ConversationID: c.ID}, func(a Activity) int64 {
		t.Helper()
		a.Worker = "mainagent"
		id, err := d.AppendConvActivity(c.ID, a)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
}

func TestToolCallsPairingFiltersAndPaging(t *testing.T) {
	d, scope, add := toolCallsFixture(t)
	use := add(Activity{Kind: "tool_use", Tool: "Bash", ToolUseID: "same", Detail: `{"command":"first"}`})
	firstResult := add(Activity{Kind: "tool_result", Tool: "Bash", ToolUseID: "same", Detail: "first result"})
	second := add(Activity{Kind: "tool_use", Tool: "Bash", ToolUseID: "same"})
	// The next result is deliberately separated by more than an activity page.
	for range 60 {
		add(Activity{Kind: "text", Summary: "filler"})
	}
	secondResult := add(Activity{Kind: "tool_result", Tool: "Bash", ToolUseID: "same", IsError: true, Detail: "Blocked by hook"})
	orphan := add(Activity{Kind: "tool_result", Tool: "mcp__fixture__read", ToolUseID: "orphan", Detail: "orphan"})
	missing := add(Activity{Kind: "tool_use", Tool: "unknown_historical", ToolUseID: "missing"})
	add(Activity{Kind: "result"})
	running := add(Activity{Kind: "tool_use", Tool: "Bash", ToolUseID: "live"})
	query := ToolCallQuery{Limit: 2, Running: true, BuiltinNames: []string{"Bash"}}
	p, err := d.ListToolCalls(t.Context(), scope, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 || !p.HasMore || p.Items[0].ID != running || p.Items[0].Status != "running" || p.Items[1].ID != missing || p.Items[1].Status != "missing" {
		t.Fatalf("latest %+v", p)
	}
	query.Snapshot = p.SnapshotCursor
	query.Before = p.Items[1].ID
	p, err = d.ListToolCalls(t.Context(), scope, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 || p.Items[0].ID != orphan || p.Items[0].UseID != nil || p.Items[0].Type != "mcp" || p.Items[1].ID != second || *p.Items[1].ResultID != secondResult || p.Items[1].Status != "failed" {
		t.Fatalf("middle %+v", p)
	}
	query.Before = second
	p, err = d.ListToolCalls(t.Context(), scope, query)
	if err != nil {
		t.Fatal(err)
	}
	if p.HasMore || len(p.Items) != 1 || p.Items[0].ID != use || *p.Items[0].ResultID != firstResult {
		t.Fatalf("oldest %+v", p)
	}
	query = ToolCallQuery{Limit: 20, Status: "failed", Q: "bAs", Type: "builtin", BuiltinNames: []string{"Bash"}}
	p, err = d.ListToolCalls(t.Context(), scope, query)
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != second {
		t.Fatalf("filters %+v %v", p, err)
	}
	query.Status = ""
	query.Q = "no match"
	p, err = d.ListToolCalls(t.Context(), scope, query)
	if err != nil || p.Items == nil || len(p.Items) != 0 || p.SnapshotCursor == 0 {
		t.Fatalf("empty %+v %v", p, err)
	}
	// A new use cannot enter a stable old page; a late result must still pair.
	snapshot := p.SnapshotCursor
	late := add(Activity{Kind: "tool_result", Tool: "Bash", ToolUseID: "live"})
	add(Activity{Kind: "tool_use", Tool: "Bash", ToolUseID: "new"})
	p, err = d.ListToolCalls(t.Context(), scope, ToolCallQuery{Limit: 20, Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if p.Items[0].ID != running || p.Items[0].Status != "success" || *p.Items[0].ResultID != late {
		t.Fatalf("late result %+v", p)
	}
}

func TestToolCallsDetailUnicodeIsolationAndReadOnly(t *testing.T) {
	d, scope, add := toolCallsFixture(t)
	body := strings.Repeat("中文😀<script>alert(1)</script>", 1000)
	id := add(Activity{Kind: "tool_use", Tool: "Bash", ToolUseID: "long", Detail: body})
	var rebuilt strings.Builder
	for offset := 0; ; {
		p, err := d.ToolCallDetail(t.Context(), scope, id, offset, 101)
		if err != nil {
			t.Fatal(err)
		}
		rebuilt.WriteString(p.Text)
		if !p.HasMore {
			break
		}
		offset = p.NextOffset
	}
	if rebuilt.String() != body {
		t.Fatal("Unicode chunking lost data")
	}
	other, err := d.CreateConversation("mainagent", "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteConversation(other.ID)
	_, err = d.ToolCallDetail(t.Context(), ToolCallScope{ConversationID: other.ID}, id, 0, 100)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross conversation detail: %v", err)
	}
	var before, after int
	if err = d.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1`, scope.ConversationID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err = d.ListToolCalls(t.Context(), scope, ToolCallQuery{Limit: 20}); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1`, scope.ConversationID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("read view wrote activity")
	}
	if _, err = d.ListToolCalls(t.Context(), scope, ToolCallQuery{Limit: 51}); err == nil {
		t.Fatal("invalid limit accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = d.ListToolCalls(ctx, scope, ToolCallQuery{Limit: 20}); err == nil {
		t.Fatal("cancelled query concealed as empty")
	}
}

func TestToolCallsTaskSessionIsolation(t *testing.T) {
	d, _, _ := toolCallsFixture(t)
	exp, err := d.CreateExploration("tool session", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	s := d.Exploration(exp)
	intent, err := s.AddIntent(map[string]any{"summary": "test"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	seg := 1
	owners := []Activity{{Worker: "mainagent"}, {Worker: "mainagent", MainSeg: &seg}, {Worker: "planner"}, {Worker: "work#1", NodeID: &intent}}
	filters := []ActivitySessionFilter{{Main: true}, {Main: true, MainSeg: &seg}, {Worker: "planner"}, {NodeID: &intent}}
	var ids []int64
	for _, a := range owners {
		a.Kind = "tool_use"
		a.ToolUseID = "reused"
		a.Tool = "Bash"
		id, err := s.AppendActivity(a)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for i, filter := range filters {
		p, err := d.ListToolCalls(t.Context(), s.ToolCallScope(filter), ToolCallQuery{Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Items) != 1 || p.Items[0].ID != ids[i] {
			t.Fatalf("session %d %+v", i, p)
		}
		_, err = d.ToolCallDetail(t.Context(), s.ToolCallScope(filter), ids[(i+1)%len(ids)], 0, 10)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("session %d leaked detail: %v", i, err)
		}
	}
}

func TestToolCallsEpochsAndCustomTypes(t *testing.T) {
	d, scope, add := toolCallsFixture(t)
	key := fmt.Sprintf("tool_call_custom_%d", scope.ConversationID)
	if err := d.SeedTool(key, "fixture", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tools SET system=false,kind='command' WHERE key=$1`, key); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM tools WHERE key=$1`, key)
	add(Activity{Kind: "tool_use", Tool: key, ToolUseID: "old"})
	add(Activity{Kind: "result"})
	orphan := add(Activity{Kind: "tool_result", Tool: key, ToolUseID: "old", IsError: true})
	add(Activity{Kind: "tool_use", Tool: "Bash"})
	add(Activity{Kind: "tool_result", Tool: "Bash"})
	p, err := d.ListToolCalls(t.Context(), scope, ToolCallQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 4 {
		t.Fatalf("missing IDs paired by name or round: %+v", p)
	}
	p, err = d.ListToolCalls(t.Context(), scope, ToolCallQuery{Limit: 20, Type: "custom", Status: "failed"})
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != orphan || p.Items[0].UseID != nil {
		t.Fatalf("custom orphan %+v %v", p, err)
	}
}

func TestToolCallsLargeHistoryProjection(t *testing.T) {
	d, scope, _ := toolCallsFixture(t)
	// One SQL fixture insertion, not thousands of round trips. The list must stay
	// small even when all 2,000 result bodies are large.
	_, err := d.Exec(`INSERT INTO conversation_activities(conversation_id,worker,kind,tool,tool_use_id,detail)
	 SELECT $1,'mainagent',CASE WHEN n%2=1 THEN 'tool_use' ELSE 'tool_result' END,'Bash',((n+1)/2)::text,repeat('大',8000)
	 FROM generate_series(1,4000) n`, scope.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	p, err := d.ListToolCalls(t.Context(), scope, ToolCallQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(p)
	if len(p.Items) != 20 || !p.HasMore || len(body) > 12000 || strings.Contains(string(body), "大") {
		t.Fatalf("unbounded list %d bytes", len(body))
	}
	t.Logf("2000 calls: %s, %d response bytes, one list SQL", time.Since(started), len(body))
	for _, count := range []int{20, 2000} {
		var plan string
		err = d.QueryRow(fmt.Sprintf(`EXPLAIN (FORMAT JSON) SELECT id FROM conversation_activities WHERE conversation_id=$1 AND kind IN ('tool_use','tool_result') ORDER BY id DESC LIMIT %d`, count), scope.ConversationID).Scan(&plan)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("query plan %d: %s", count, plan)
	}
}
