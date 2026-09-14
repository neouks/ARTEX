package agentcore

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

type deferredPolicyProvider struct {
	name  string
	input json.RawMessage
	calls int
}

func (p *deferredPolicyProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	if p.calls == 1 {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "probe-call", Name: p.name, Input: p.input}}}, "tool_use", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}}, "end_turn", llm.Usage{}, nil
}
func (*deferredPolicyProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	panic("expected non-streaming test")
}

func TestSessionPropagatesDeferredExecutionGate(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, unlocked := range []bool{false, true} {
			name := "direct"
			if wrapped {
				name = "wrapped"
			}
			if unlocked {
				name += "/unlocked"
			} else {
				name += "/locked"
			}
			t.Run(name, func(t *testing.T) {
				calls := 0
				probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object"}, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
					return permission.Allowed()
				}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
					calls++
					return tool.Text("executed"), nil
				}})
				gate := tool.NewUnlockSet()
				p := &deferredPolicyProvider{name: "probe", input: json.RawMessage(`{}`)}
				if wrapped {
					p.name = tool.ExecuteExtraToolName
					p.input = json.RawMessage(`{"tool_name":"probe"}`)
				}
				s := NewSession(Options{Provider: p, Tools: []tool.CoreTool{probe}, DeferredTools: []string{"probe"}, UnlockSet: gate, NonStreaming: true, PermissionMode: permission.ModeBypass, MaxTurns: 3, DisableBackgroundTasks: true})
				defer s.Close()
				// Unlock after session construction to verify it shares the live gate.
				if unlocked {
					gate.Add("probe")
				}
				for _, err := range s.Prompt(context.Background(), "run") {
					if err != nil {
						t.Fatal(err)
					}
				}
				if (calls == 1) != unlocked {
					t.Fatalf("unexpected execution count=%d", calls)
				}
				seen := false
				for _, m := range s.Messages() {
					for _, b := range m.Content {
						if b.Type == llm.BlockToolResult && b.ToolUseID == "probe-call" {
							seen = true
							if b.IsError == unlocked {
								t.Fatal("wrong result status")
							}
						}
					}
				}
				if !seen {
					t.Fatal("lost call/result pairing")
				}
			})
		}
	}
}
