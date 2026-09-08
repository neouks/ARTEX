package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPlannerToolContractMatchesPrefetchedPrompt(t *testing.T) {
	tools := (&ToolSet{}).PlannerTools()
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	if !names["list_assets"] {
		t.Fatal("planner prompt advertises list_assets but PlannerTools omitted it")
	}
	for _, redundant := range []string{"graph_overview", "list_goals", "goal_met", "asset_neighbors"} {
		if names[redundant] {
			t.Fatalf("planner should not expose redundant or nonexistent tool %q", redundant)
		}
	}
}

func TestWorkerLocalToolsExcludePollingSleep(t *testing.T) {
	names := make(map[string]bool)
	for _, tool := range workerLocalTools() {
		names[tool.Name()] = true
	}
	if !names["Bash"] || !names["Read"] || names["Sleep"] {
		t.Fatalf("unexpected worker local tool set: %v", names)
	}
}

func TestAddIntentRejectsBatchOverFourBeforeWriting(t *testing.T) {
	input := json.RawMessage(`{"intents":[{"summary":"1"},{"summary":"2"},{"summary":"3"},{"summary":"4"},{"summary":"5"}]}`)
	result, err := (&ToolSet{}).addIntent().Call(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Flatten(), "最多新增 4 条") {
		t.Fatalf("oversized batch result = error:%v text:%q", result.IsError, result.Flatten())
	}
}
