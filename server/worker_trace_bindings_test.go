package server

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestWorkerTraceBindingsMigration(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test database not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	s := &Server{m: &Manager{pg: pg}}
	const flag = "worker_targeted_trace_bindings_v1"
	for _, key := range []string{flag, "worker_readtools_unbind_v1"} {
		value, exists, err := pg.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = pg.SetSetting(key, value)
			} else {
				_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, key)
			}
		})
	}
	for _, key := range []string{"search_all_worker_traces", "get_worker_trace", "list_facts", "node_detail", "list_companies", "list_worker_traces"} {
		old, err := pg.GetTool(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if old == nil {
				_, _ = pg.Exec(`DELETE FROM tools WHERE key=$1`, key)
				return
			}
			bindings, _ := json.Marshal(old.Agents)
			_ = pg.UpdateTool(key, old.Description, old.Schema, bindings, old.Enabled)
		})
	}
	_, err = pg.Exec(`DELETE FROM settings WHERE key=$1`, flag)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range agent.BuiltinToolSeeds() {
		if seed.Key != "search_all_worker_traces" && seed.Key != "get_worker_trace" {
			continue
		}
		schema, _ := json.Marshal(seed.Schema)
		if err := pg.UpsertToolForce(seed.Key, seed.Desc, schema, json.RawMessage(`["planner","mainagent"]`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pg.Exec(`UPDATE tools SET enabled=false WHERE key='get_worker_trace'`); err != nil {
		t.Fatal(err)
	}
	// Reproduce an old database before the unbind migration ran as well.
	_, _ = pg.Exec(`DELETE FROM settings WHERE key='worker_readtools_unbind_v1'`)
	s.seedWorkerReadToolsUnbind()
	if err := s.seedWorkerTraceBindings(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"search_all_worker_traces", "get_worker_trace"} {
		row, err := pg.GetTool(name)
		if err != nil || row == nil {
			t.Fatal(err)
		}
		if !slices.Contains(row.Agents, "worker") || !slices.Contains(row.Agents, "planner") || !slices.Contains(row.Agents, "mainagent") {
			t.Fatal("lost binding", row.Agents)
		}
		if name == "get_worker_trace" && row.Enabled {
			t.Fatal("overrode disabled tool")
		}
	}
	if err := pg.RemoveAgentFromTool("worker", "get_worker_trace"); err != nil {
		t.Fatal(err)
	}
	if err := s.seedWorkerTraceBindings(); err != nil {
		t.Fatal(err)
	}
	row, _ := pg.GetTool("get_worker_trace")
	if slices.Contains(row.Agents, "worker") {
		t.Fatal("repeated migration overrode manual unbinding")
	}
}
