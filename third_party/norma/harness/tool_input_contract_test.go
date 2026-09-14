package harness

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

type inputContractHooks struct {
	seen    []byte
	updated []byte
}

func (h *inputContractHooks) PreToolUse(_ context.Context, _ string, in []byte) (bool, string, []byte) {
	h.seen = append([]byte(nil), in...)
	return false, "", h.updated
}
func (*inputContractHooks) PostToolUse(context.Context, string, []byte, []byte, bool) {}
func (*inputContractHooks) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return false, nil, ""
}

func TestEffectiveInputDefaultsAndHookValidation(t *testing.T) {
	ctx := context.Background()
	for _, invalid := range []bool{false, true} {
		calls := 0
		h := &inputContractHooks{}
		if invalid {
			h.updated = []byte(`{"limit":"bad"}`)
		}
		base := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object", "required": []string{"limit"}, "properties": map[string]any{"limit": map[string]any{"type": "integer", "default": 3, "maximum": 5}}}, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		}, Run: func(_ context.Context, raw json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			calls++
			var in map[string]any
			json.Unmarshal(raw, &in)
			if in["limit"] != float64(3) {
				t.Error("execution lost default")
			}
			return tool.Text("ok"), nil
		}})
		l := &loop{ctx: ctx, in: QueryInput{Tools: tool.NewRegistry(base), Hooks: h}}
		result, _ := l.execOne(ctx, false, llm.ContentBlock{ID: "id", Name: "probe", Input: json.RawMessage(`{}`)}, nil)
		var seen map[string]any
		json.Unmarshal(h.seen, &seen)
		if seen["limit"] != float64(3) {
			t.Fatal("hook did not see effective default")
		}
		if invalid && (calls != 0 || !result.IsError) {
			t.Fatal("invalid hook update executed")
		}
		if !invalid && (calls != 1 || result.IsError) {
			t.Fatal("valid default rejected")
		}
	}
}
