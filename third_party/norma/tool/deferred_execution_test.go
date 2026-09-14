package tool

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/norma/permission"
	"testing"
)

func TestDeferredExecutionRequiresPolicyDispatcher(t *testing.T) {
	for _, scenario := range []string{"bad_json", "missing_name", "unknown", "locked", "invalid_params", "missing_context", "missing_executor", "default_params", "supplied_params"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			probe := Build(Spec{Name: "probe", Schema: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer", "default": 2}}}, Run: func(context.Context, json.RawMessage, *ToolContext) (Result, error) {
				t.Fatal("bypassed dispatcher")
				return Result{}, nil
			}})
			reg := NewRegistry(probe)
			unlock := NewUnlockSet("probe")
			if scenario == "locked" {
				unlock = NewUnlockSet()
			}
			if scenario == "default_params" {
				unlock = nil
			}
			raw := json.RawMessage(`{"tool_name":"probe"}`)
			switch scenario {
			case "bad_json":
				raw = json.RawMessage(`!`)
			case "missing_name":
				raw = json.RawMessage(`{}`)
			case "unknown":
				raw = json.RawMessage(`{"tool_name":"missing"}`)
			case "invalid_params":
				raw = json.RawMessage(`{"tool_name":"probe","params":{"n":"bad"}}`)
			case "supplied_params":
				raw = json.RawMessage(`{"tool_name":"probe","params":{"n":4}}`)
			}
			tc := &ToolContext{ExecuteTool: func(_ context.Context, name string, params json.RawMessage) (Result, error) {
				calls++
				want := `{"n":2}`
				if scenario == "supplied_params" {
					want = `{"n":4}`
				}
				if name != "probe" || string(params) != want {
					t.Fatal("invalid dispatch", name, string(params))
				}
				return Text("dispatched"), nil
			}}
			if scenario == "missing_context" {
				tc = nil
			}
			if scenario == "missing_executor" {
				tc = &ToolContext{}
			}
			wrapper := NewExecuteExtraTool(reg, unlock)
			if wrapper.CheckPermissions(context.Background(), raw, permission.Context{}).Behavior != permission.Allow {
				t.Fatal("wrapper permission changed")
			}
			r, err := wrapper.Call(context.Background(), raw, tc)
			allowed := scenario == "default_params" || scenario == "supplied_params"
			if err != nil || r.IsError == allowed {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			if (calls == 1) != allowed {
				t.Fatal("unexpected dispatch count", calls)
			}
		})
	}
}
