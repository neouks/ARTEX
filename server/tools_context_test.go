package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func TestInjectedGraphOverviewUsesRunStore(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	old := agent.ToolResolve
	t.Cleanup(func() { agent.ToolResolve = old })
	reg := buildDomainReg(pg.Assets())
	for _, key := range []string{"graph_overview", "list_facts", "list_findings", "node_detail", "expand_digest", "record_fact"} {
		if reg[key] != nil {
			t.Fatalf("standalone registry contains task-only %s", key)
		}
	}
	wireTools(pg, reg)
	row, err := pg.GetTool("graph_overview")
	if err != nil || row == nil {
		t.Fatalf("graph catalog: %v", err)
	}
	t.Cleanup(func() { _ = pg.UpdateTool(row.Key, row.Description, row.Schema, mustJSON(row.Agents), row.Enabled) })
	if err := pg.UpdateTool(row.Key, row.Description, row.Schema, mustJSON([]string{"planner"}), true); err != nil {
		t.Fatal(err)
	}
	if tools := names(agent.ToolResolve(context.Background(), "planner", nil)); tools["graph_overview"] != nil {
		t.Fatal("injected unbound graph")
	}
	var contexts []context.Context
	for i := 0; i < 2; i++ {
		task, err := pg.CreateTask(fmt.Sprintf("graph-run-%d", i), fmt.Sprintf("unique-goal-%d", i), nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer pg.DeleteTask(task.ID)
		ts := agent.NewToolSet(pg.Exploration(task.ExplorationID), "planner")
		ts.SetTaskID(task.ID)
		ts.SetAssetStore(pg.Assets(), pg.Companies())
		ctx := agent.WithRunInfo(t.Context(), agent.RunInfo{TaskID: task.ID, ExplorationID: task.ExplorationID})
		if tools := names(agent.ToolResolve(ctx, "planner", nil)); tools["graph_overview"] != nil || tools["list_assets"] != nil {
			t.Fatal("missing task tools fell back to global")
		}
		contexts = append(contexts, agent.WithTaskToolSet(ctx, ts))
	}
	// Missing base reproduces the exact catalog-injection path from the panic.
	for i, ctx := range contexts {
		tools := names(agent.ToolResolve(ctx, "planner", nil))
		graph := tools["graph_overview"]
		if graph == nil {
			t.Fatal("task graph tool not injected")
		}
		result, err := graph.Call(ctx, json.RawMessage(`{}`), nil)
		if err != nil || result.IsError {
			t.Fatalf("graph failed: %+v %v", result, err)
		}
		if !strings.Contains(result.Flatten(), fmt.Sprintf("unique-goal-%d", i)) || strings.Contains(result.Flatten(), fmt.Sprintf("unique-goal-%d", 1-i)) {
			t.Fatalf("wrong task context: %s", result.Flatten())
		}
		// A per-run base handler (and its callbacks) must retain priority.
		base := []actool.CoreTool{graph}
		if got := names(agent.ToolResolve(ctx, "planner", base)); got["graph_overview"] == nil {
			t.Fatal("base graph disappeared")
		}
	}
}
