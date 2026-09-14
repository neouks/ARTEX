package server

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Autumn-27/artex/agent"
	actool "github.com/Autumn-27/norma/tool"
)

func TestTaskWrapperSchemaMatchesLocalTool(t *testing.T) {
	s := &Server{}
	ts := agent.NewToolSet(nil, "")
	pairs := [][2]actool.CoreTool{{s.toolGetTaskGraph(), ts.GraphOverviewTool()}, {s.toolListTaskFindings(), ts.ListFindingsTool()}, {s.toolAddHint(), ts.AddHintTool()}, {s.toolGetWorkerTrace(), ts.GetWorkerTraceTool()}, {s.toolListWorkerTraces(), ts.ListWorkerTracesTool()}, {s.toolSearchWorkerTraces(), ts.SearchWorkerTracesTool()}, {s.toolGetTaskNodeDetail(), ts.NodeDetailTool()}}
	for _, pair := range pairs {
		normalize := func(v any) map[string]any {
			raw, _ := json.Marshal(v)
			var out map[string]any
			json.Unmarshal(raw, &out)
			return out
		}
		wrapped, local := normalize(pair[0].InputSchema()), normalize(pair[1].InputSchema())
		props := wrapped["properties"].(map[string]any)
		delete(props, "task_id")
		var required []any
		for _, value := range wrapped["required"].([]any) {
			if value != "task_id" {
				required = append(required, value)
			}
		}
		if len(required) == 0 {
			delete(wrapped, "required")
		} else {
			wrapped["required"] = required
		}
		if !reflect.DeepEqual(wrapped, local) {
			t.Fatalf("%s schema diverged", pair[0].Name())
		}
		if pair[0].IsReadOnly(nil) != pair[1].IsReadOnly(nil) {
			t.Fatal("wrapper changed write semantics")
		}
	}
}

func TestHostToolCatalogContracts(t *testing.T) {
	s := &Server{}
	tools := append(s.orchestrationTools(), s.platformTools()...)
	seen := map[string]bool{}
	for _, tool := range tools {
		if seen[tool.Name()] || tool.Name() == "" {
			t.Fatalf("duplicate/empty host name %q", tool.Name())
		}
		seen[tool.Name()] = true
		raw, err := json.Marshal(tool.InputSchema())
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		json.Unmarshal(raw, &schema)
		props, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if props[key.(string)] == nil {
					t.Fatalf("%s required field %v is absent", tool.Name(), key)
				}
			}
		}
	}
	t.Logf("checked %d orchestration/platform tools", len(tools))
}
