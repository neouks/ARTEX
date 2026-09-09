package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

type recordingMCPUsage struct {
	rows []*db.MCPUsage
	err  error
}

func (r *recordingMCPUsage) InsertMCPUsage(row *db.MCPUsage) error {
	copy := *row
	r.rows = append(r.rows, &copy)
	return r.err
}

func TestMeterMCPToolRecordsBeforeSuccessfulCall(t *testing.T) {
	calls := 0
	base := actool.Build(actool.Spec{
		Name: "mcp__demo__search",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			calls++
			return actool.Result{}, nil
		},
	})
	recorder := &recordingMCPUsage{}
	ri := agent.RunInfo{TaskID: 42, ExplorationID: 8, IntentID: 9, SessionID: "session-1"}
	wrapped := meterMCPTool(base, recorder, 7, "demo", "worker", ri)

	if _, err := wrapped.Call(context.Background(), json.RawMessage(`{"secret":"not persisted"}`), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(recorder.rows) != 1 {
		t.Fatalf("call/ledger counts: calls=%d rows=%d", calls, len(recorder.rows))
	}
	got := recorder.rows[0]
	if got.ServerID != 7 || got.ServerName != "demo" || got.ToolName != "search" || got.AgentKey != "worker" ||
		got.TaskID != 42 || got.ExplorationID != 8 || got.IntentID != 9 || got.SessionID != "session-1" {
		t.Fatalf("unexpected MCP attribution: %+v", got)
	}
}

func TestMeterMCPToolCountsFailedCallAndHidesLedgerError(t *testing.T) {
	base := actool.Build(actool.Spec{
		Name: "mcp__demo__login",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Result{}, errors.New("authentication failed")
		},
	})
	recorder := &recordingMCPUsage{err: errors.New("ledger unavailable")}
	wrapped := meterMCPTool(base, recorder, 7, "demo", "worker", agent.RunInfo{})

	if _, err := wrapped.Call(context.Background(), nil, nil); err == nil || err.Error() != "authentication failed" {
		t.Fatalf("delegate error should be preserved, got %v", err)
	}
	if len(recorder.rows) != 1 {
		t.Fatalf("failed call should still be counted once, got %d", len(recorder.rows))
	}
}
