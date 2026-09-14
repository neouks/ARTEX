package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestBuiltinSchemaCannotBeReplacedByStoredVersion(t *testing.T) {
	base := NewToolSet(nil, "worker").listFindings()
	stored := map[string]any{"type": "array", "properties": map[string]any{
		"limit":   map[string]any{"type": "string", "default": "bad"},
		"removed": map[string]any{"type": "string"},
	}}
	tool := ResolveBuiltinTool(base, "返回全部漏洞", stored)
	if tool.Description() != base.Description() {
		t.Fatal("old protocol replaced code contract")
	}
	if tool.InputSchema()["type"] != "object" {
		t.Fatal("type overridden")
	}
	props := tool.InputSchema()["properties"].(map[string]any)
	if props["before"] == nil || props["removed"] != nil {
		t.Fatal("schema drift")
	}
	if _, ok := props["limit"].(map[string]any)["default"].(string); ok {
		t.Fatal("invalid default retained")
	}
}

func TestFactBatchStructureAndDecodeFailBeforeWriting(t *testing.T) {
	tool := NewToolSet(nil, "worker").recordFact()
	for _, input := range []string{`{"facts":[{"summary":42}]}`, `{"facts":[{"summary":"ok"}, {"summary":"bad","confidence":{}}]}`} {
		if err := actool.ValidateInput(tool.InputSchema(), json.RawMessage(input)); err == nil {
			t.Fatal("nested invalid type accepted")
		}
		// nil store would panic if any write happened before decoding completed.
		result, err := tool.Call(context.Background(), json.RawMessage(input), nil)
		if err != nil || !result.IsError {
			t.Fatal("decode error not returned")
		}
	}
	if err := actool.ValidateInput(tool.InputSchema(), json.RawMessage(`{"facts":[{"summary":"ok","confidence":"observed"}]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredRoleTools(t *testing.T) {
	ts := NewToolSet(nil, "")
	for role, tools := range map[string][]actool.CoreTool{"worker": ts.WorkerTools(), "planner": ts.PlannerTools()} {
		if err := requireRoleTools(role, tools); err != nil {
			t.Fatal(err)
		}
		if err := requireRoleTools(role, nil); err == nil {
			t.Fatal("missing core tools allowed")
		}
	}
}

func TestAssetQueryContractSurvivesStoredMetadata(t *testing.T) {
	base := NewToolSet(nil, "worker").listAssets()
	stored := map[string]any{"description": "只传 limit 即可", "properties": map[string]any{
		"dsl":   map[string]any{"description": "可省略", "default": ""},
		"limit": map[string]any{"description": "可单独使用", "default": 50},
	}}
	resolved := ResolveBuiltinTool(base, "只传 limit 即可", stored)
	want, _ := json.Marshal(base.InputSchema())
	got, _ := json.Marshal(resolved.InputSchema())
	if !reflect.DeepEqual(want, got) || resolved.Description() != base.Description() {
		t.Fatal("stored metadata replaced asset query instructions")
	}
}
