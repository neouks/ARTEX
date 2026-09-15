package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestToolCatalogUsageResponse(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	const key = "test_tool_catalog_usage"
	defer pg.Exec(`DELETE FROM tools WHERE key=$1`, key)
	defer pg.Exec(`DELETE FROM public.tool_usage WHERE tool_key=$1`, key)
	if err = pg.SeedTool(key, "usage test", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = pg.InsertToolUsage(&db.ToolUsage{ToolKey: key}); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}}
	w := httptest.NewRecorder()
	s.pgListTools(w, httptest.NewRequest("GET", "/api/tools", nil))
	var response struct {
		Tools []*db.Tool `json:"tools"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatalf("tool response: %d %s", w.Code, w.Body.String())
	}
	found := false
	for _, tool := range response.Tools {
		if tool.Key == key {
			found = true
			if tool.Calls != 1 {
				t.Fatalf("calls=%d want=1", tool.Calls)
			}
		}
	}
	if !found {
		t.Fatal("tool missing from catalog")
	}
	// Shadow only this connection's ledger to simulate an aggregation failure;
	// the catalog remains readable and no real table is modified.
	pg.SetMaxOpenConns(1)
	pg.SetMaxIdleConns(1)
	if _, err = pg.Exec(`CREATE TEMP TABLE tool_usage (unavailable integer)`); err != nil {
		t.Fatal(err)
	}
	defer pg.Exec(`DROP TABLE pg_temp.tool_usage`)
	w = httptest.NewRecorder()
	s.pgListTools(w, httptest.NewRequest("GET", "/api/tools", nil))
	if w.Code != 500 {
		t.Fatalf("aggregation failure returned %d: %s", w.Code, w.Body.String())
	}
}

type recordingToolUsage struct {
	rows []*db.ToolUsage
	err  error
}

func (r *recordingToolUsage) InsertToolUsage(row *db.ToolUsage) error {
	copy := *row
	r.rows = append(r.rows, &copy)
	return r.err
}

func TestMeterToolRecordsAttributionAndDelegates(t *testing.T) {
	calls := 0
	base := actool.Build(actool.Spec{
		Name: "record_fact",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			calls++
			return actool.Result{}, nil
		},
	})
	recorder := &recordingToolUsage{}
	ri := agent.RunInfo{TaskID: 42, ExplorationID: 8, IntentID: 9, SessionID: "session-1"}
	wrapped := meterTool(base, recorder, "record_fact", "worker", ri)

	if _, err := wrapped.Call(context.Background(), json.RawMessage(`{"secret":"not persisted"}`), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("delegate calls: want 1, got %d", calls)
	}
	if len(recorder.rows) != 1 {
		t.Fatalf("usage rows: want 1, got %d", len(recorder.rows))
	}
	got := recorder.rows[0]
	if got.ToolKey != "record_fact" || got.AgentKey != "worker" || got.TaskID != 42 ||
		got.ExplorationID != 8 || got.IntentID != 9 || got.SessionID != "session-1" {
		t.Fatalf("unexpected attribution: %+v", got)
	}
}

func TestMeterToolFailureDoesNotBreakInvocation(t *testing.T) {
	calls := 0
	base := actool.Build(actool.Spec{
		Name: "list_facts",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			calls++
			return actool.Result{}, nil
		},
	})
	recorder := &recordingToolUsage{err: errors.New("ledger unavailable")}
	wrapped := meterTool(base, recorder, "list_facts", "worker", agent.RunInfo{})

	if _, err := wrapped.Call(context.Background(), nil, nil); err != nil {
		t.Fatalf("metering error leaked into tool call: %v", err)
	}
	if calls != 1 {
		t.Fatalf("delegate calls: want 1, got %d", calls)
	}
}
