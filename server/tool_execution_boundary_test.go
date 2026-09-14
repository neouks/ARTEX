package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	actool "github.com/Autumn-27/norma/tool"
)

func TestToolCatalogReadFailureIsClosed(t *testing.T) {
	// A closed pool needs no running database and cannot touch business data.
	pool, err := sql.Open("pgx", "postgres://unused:unused@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	oldResolve, oldBinding := agent.ToolResolve, agent.FindingTrafficBindingEnabled
	t.Cleanup(func() { agent.ToolResolve = oldResolve; agent.FindingTrafficBindingEnabled = oldBinding })
	wireTools(&db.DB{DB: pool}, nil)
	for _, role := range []string{"worker", "planner", "mainagent", "retester", "custom"} {
		tools, err := agent.ToolResolve(context.Background(), role, []actool.CoreTool{actool.NewBash()})
		if err == nil || len(tools) != 0 || !strings.Contains(err.Error(), "停止本次工具装配") {
			t.Fatalf("fail-open for %s: %v", role, err)
		}
	}
}

func TestRetestDetailRejectsInputBeforeDB(t *testing.T) {
	s := &Server{}
	tool := s.findingRetestTools()[0]
	for _, raw := range []string{`null`, `[]`, `{"task_id":1}`, `{"offset":-1}`, `{"max_chars":0}`, `{"max_chars":24001}`, `{"field":"not-a-pointer"}`, `{"offset":null}`, `{} {}`} {
		r, err := tool.Call(context.Background(), json.RawMessage(raw), nil)
		if err != nil || !r.IsError {
			t.Fatal("invalid request reached DB", raw, err)
		}
	}
}

func TestRetestDetailPagingAndConversationScope(t *testing.T) {
	s, fid := newRetestServer(t)
	task, err := s.m.pg.CreateTask("unloaded retest source", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.DeleteTask(task.ID) })
	if _, err := s.m.pg.Exec(`UPDATE findings SET task_id=$1 WHERE id=$2`, task.ID, fid); err != nil {
		t.Fatal(err)
	}
	r, conv, _, err := s.m.pg.CreateFindingRetest(t.Context(), fid, "仅复测原漏洞")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("中文证据😀\"\n\x01", 15000)
	snapshot, _ := json.Marshal(map[string]any{"finding": map[string]any{"evidence": text}, "assets": []any{}})
	if _, err := s.m.pg.Exec(`UPDATE finding_retests SET snapshot=$1 WHERE id=$2`, snapshot, r.ID); err != nil {
		t.Fatal(err)
	}
	ctx := intercept.WithConvID(t.Context(), conv.ID)
	tool := s.findingRetestTools()[0]
	if _, err := s.m.pg.Exploration(task.ExplorationID).AddConstraint("deny", "新增约束：禁止删除数据", "human"); err != nil {
		t.Fatal(err)
	}
	call := func(ctx context.Context, params map[string]any) actool.Result {
		raw, _ := json.Marshal(params)
		result, err := tool.Call(ctx, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := call(ctx, map[string]any{})
	t.Logf("source_snapshot_bytes=%d overview_response_bytes=%d", len(snapshot), len(first.Flatten()))
	if !strings.Contains(first.Flatten(), "新增约束：禁止删除数据") {
		t.Fatal("current constraints were omitted for an unloaded task")
	}
	if first.IsError || len([]rune(first.Flatten())) > 24000 || !strings.Contains(first.Flatten(), `"deferred":true`) {
		t.Fatal("unbounded overview", first.Flatten())
	}
	var rebuilt strings.Builder
	for offset := 0; ; {
		res := call(ctx, map[string]any{"field": "/retest/snapshot/finding/evidence", "offset": offset, "max_chars": 24000})
		if res.IsError || len([]rune(res.Flatten())) > 24000 {
			t.Fatal("bad page", res.Flatten())
		}
		var out struct {
			Value     string `json:"value"`
			Next      int    `json:"next_offset"`
			Truncated bool   `json:"truncated"`
		}
		if err := json.Unmarshal([]byte(res.Flatten()), &out); err != nil {
			t.Fatal(err)
		}
		rebuilt.WriteString(out.Value)
		if !out.Truncated {
			break
		}
		if out.Next <= offset {
			t.Fatal("no progress")
		}
		offset = out.Next
	}
	if rebuilt.String() != text {
		t.Fatal("evidence changed")
	}
	if result := call(intercept.WithConvID(t.Context(), 0), map[string]any{}); !result.IsError {
		t.Fatal("unassociated conversation read snapshot")
	}
	if result := call(ctx, map[string]any{"field": "/missing"}); !result.IsError {
		t.Fatal("missing field hidden")
	}
	pool, err := sql.Open("pgx", "postgres://unused:unused@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	failed := &Server{m: &Manager{pg: &db.DB{DB: pool}}}
	result, err := failed.findingRetestTools()[0].Call(ctx, json.RawMessage(`{}`), nil)
	if err != nil || !result.IsError {
		t.Fatal("DB failure returned false empty result")
	}
}
