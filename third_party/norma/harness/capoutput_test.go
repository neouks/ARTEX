package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

// The global post-tool net caps an oversized text block regardless of the tool.
func TestCapOutputCapsOversizedTextBlock(t *testing.T) {
	tc := &tool.ToolContext{MaxOutputChars: 100} // no OutputDir → head+tail truncation
	long := strings.Repeat("z", 5000)
	res := tool.Result{Content: []llm.ContentBlock{llm.TextBlock(long)}}

	out := capOutput(tc, res)
	got := out.Content[0].Text
	if len(got) >= 5000 || !strings.Contains(got, "characters truncated]") {
		t.Fatalf("capOutput did not cap oversized block: len=%d", len(got))
	}
}

// Output already within budget is passed through untouched.
func TestCapOutputLeavesSmallOutputIntact(t *testing.T) {
	tc := &tool.ToolContext{MaxOutputChars: 100}
	res := tool.Result{Content: []llm.ContentBlock{llm.TextBlock("fine")}}

	if out := capOutput(tc, res); out.Content[0].Text != "fine" {
		t.Fatalf("small output changed: %q", out.Content[0].Text)
	}
}

// Output a tool already captured (carries the spill marker) is not re-processed,
// so the net never double-truncates.
func TestCapOutputIdempotentOnAlreadyCaptured(t *testing.T) {
	tc := &tool.ToolContext{MaxOutputChars: 100}
	pre := tool.Capture(tc, strings.Repeat("a", 5000))
	res := tool.Result{Content: []llm.ContentBlock{llm.TextBlock(pre)}}

	if out := capOutput(tc, res); out.Content[0].Text != pre {
		t.Fatalf("capOutput re-processed already-captured output")
	}
}

func TestGlobalCapPreservesResultAndSource(t *testing.T) {
	long := strings.Repeat("中文🙂", 5000)
	image := llm.ContentBlock{Type: llm.BlockThinking, Thinking: "fixture", Signature: "original signature"}
	res := tool.Result{IsError: true, Content: []llm.ContentBlock{llm.TextBlock(long), image, llm.TextBlock(long)}, Extra: []llm.Message{llm.UserText("extra")}}
	out := capOutput(&tool.ToolContext{MaxOutputChars: 11}, res)
	if !out.IsError || !reflect.DeepEqual(out.Extra, res.Extra) || !reflect.DeepEqual(out.Content[1], image) {
		t.Fatal("lost result metadata")
	}
	if res.Content[0].Text != long || res.Content[2].Text != long {
		t.Fatal("mutated original tool result")
	}
	for _, i := range []int{0, 2} {
		if len(out.Content[i].Text) > 100 || !utf8.ValidString(out.Content[i].Text) {
			t.Fatal("unbounded or invalid text block")
		}
	}
}

func TestGlobalCapDirectAndDeferredExecution(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		for _, captured := range []bool{false, true} {
			t.Run(fmt.Sprintf("deferred=%t/captured=%t", deferred, captured), func(t *testing.T) {
				dir := t.TempDir()
				calls := 0
				probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object"}, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
					return permission.Allowed()
				}, Run: func(_ context.Context, _ json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
					calls++
					body := strings.Repeat("中文 response", 5000)
					if captured {
						body = tool.Capture(tc, body)
					}
					result := tool.Text(body)
					result.IsError = true
					result.Extra = []llm.Message{llm.UserText("extra")}
					return result, nil
				}})
				hooks := &executionHooks{}
				unlock := tool.NewUnlockSet("probe")
				reg := tool.NewRegistry(probe)
				reg.Add(tool.NewExecuteExtraTool(reg, unlock))
				in := QueryInput{Tools: reg, Hooks: hooks, ToolOutputDir: dir, MaxToolOutputChars: 100, PermissionMode: permission.ModeBypass, UnlockSet: unlock}
				name, raw := "probe", json.RawMessage(`{}`)
				if deferred {
					in.DeferredTools = []string{"probe"}
					name = tool.ExecuteExtraToolName
					raw = json.RawMessage(`{"tool_name":"probe","params":{}}`)
				}
				l := &loop{ctx: t.Context(), in: in}
				result, extra := l.execOne(t.Context(), false, llm.ContentBlock{ID: "call", Name: name, Input: raw}, nil)
				if !result.IsError || calls != 1 || len(hooks.pre) != 1 || len(hooks.post) != 1 || len(extra) != 1 || !strings.Contains(result.Content[0].Text, "<persisted-output>") {
					t.Fatal("execution semantics changed", result, calls, hooks)
				}
				files, err := os.ReadDir(dir)
				if err != nil || len(files) != 1 {
					t.Fatalf("duplicate spill: %d err=%v", len(files), err)
				}
			})
		}
	}
}
