package agent

import (
	"slices"
	"testing"
)

func TestWorkerTargetedTraceTools(t *testing.T) {
	tools := NewToolSet(nil, "worker").WorkerTools()
	names := map[string]int{}
	for _, tool := range tools {
		names[tool.Name()]++
	}
	for _, name := range []string{"search_all_worker_traces", "get_worker_trace"} {
		if names[name] != 1 {
			t.Fatalf("%s registration count=%d", name, names[name])
		}
		bound := false
		for _, seed := range BuiltinToolSeeds() {
			if seed.Key == name {
				bound = slices.Contains(seed.Agents, "worker")
			}
		}
		if !bound {
			t.Fatalf("%s missing default worker binding", name)
		}
	}
	for _, name := range []string{"list_worker_traces", "list_task_assets", "add_intent", "list_facts", "node_detail"} {
		if names[name] != 0 {
			t.Fatalf("planning tool exposed: %s", name)
		}
	}
}
