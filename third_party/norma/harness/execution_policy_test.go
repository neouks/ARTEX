package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

type executionHooks struct {
	pre, post []string
	block     bool
	panicAt   string
	updated   []byte
}

func TestExecutionPolicyResultAndPermissionEdges(t *testing.T) {
	for _, scenario := range []string{"unknown_settling", "empty_deny", "permission_update", "bad_permission_update", "run_error", "empty_output"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			calls := 0
			probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer", "default": 3}}}, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
				if scenario == "empty_deny" {
					return permission.Decision{Behavior: permission.Deny}
				}
				d := permission.Allowed()
				if scenario == "permission_update" {
					d.UpdatedInput = json.RawMessage(`{"limit":4}`)
				}
				if scenario == "bad_permission_update" {
					d.UpdatedInput = json.RawMessage(`{"limit":"bad"}`)
				}
				return d
			}, Run: func(_ context.Context, in json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
				if tc == nil || tc.ToolUseID != "id" {
					t.Fatalf("lost invocation context: %+v", tc)
				}
				calls++
				if scenario == "run_error" {
					return tool.Result{}, errors.New("synthetic error")
				}
				if scenario == "permission_update" && string(in) != `{"limit":4}` {
					t.Fatal("lost approved update", string(in))
				}
				return tool.Result{}, nil
			}})
			l := &loop{ctx: ctx, in: QueryInput{Tools: tool.NewRegistry(probe), Settlement: &Settlement{DisabledTools: []string{"probe"}}, PermissionModeFunc: func() permission.Mode { return permission.ModeDefault }}}
			name := "probe"
			if scenario == "unknown_settling" {
				name = "unknown"
			}
			r, _ := l.execOne(ctx, scenario == "unknown_settling", llm.ContentBlock{ID: "id", Name: name, Input: json.RawMessage(`{}`)}, nil)
			wantError := scenario != "permission_update" && scenario != "empty_output"
			if r.IsError != wantError {
				t.Fatalf("unexpected result: %+v", r)
			}
			if wantError && scenario != "run_error" && calls != 0 {
				t.Fatal("denied call executed")
			}
		})
	}
}

func (h *executionHooks) PreToolUse(_ context.Context, name string, _ []byte) (bool, string, []byte) {
	if h.panicAt == "pre" {
		panic("sensitive panic value")
	}
	h.pre = append(h.pre, name)
	return h.block, "policy denied", h.updated
}
func (h *executionHooks) PostToolUse(_ context.Context, name string, _, _ []byte, _ bool) {
	if h.panicAt == "post" {
		panic("sensitive panic value")
	}
	h.post = append(h.post, name)
}
func (*executionHooks) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return false, nil, ""
}

func TestExecutionPolicyDirectAndDeferred(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		label := "direct"
		if deferred {
			label = "deferred"
		}
		for _, scenario := range []string{"allowed", "locked", "nil_unlock", "disabled", "hard_deny", "disallowed", "plan_mode", "hook_deny", "bad_input", "cancelled", "hook_update", "bad_hook_update"} {
			t.Run(label+"/"+scenario, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls, checks := 0, 0
				h := &executionHooks{block: scenario == "hook_deny"}
				if scenario == "hook_update" {
					h.updated = []byte(`{"limit":4}`)
				}
				if scenario == "bad_hook_update" {
					h.updated = []byte(`{"limit":"bad"}`)
				}
				probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer", "default": 3, "minimum": 1, "maximum": 5}}}, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
					checks++
					if scenario == "hard_deny" {
						return permission.Denied("hard deny")
					}
					return permission.Allowed()
				}, Run: func(_ context.Context, in json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
					calls++
					want := `{"limit":3}`
					if scenario == "hook_update" {
						want = `{"limit":4}`
					}
					if string(in) != want {
						t.Errorf("effective input=%s want=%s", in, want)
					}
					if tc.WorkingDir != "fixture" || tc.AgentID != "worker" {
						t.Error("lost execution scope")
					}
					if tc.ExecuteTool != nil {
						t.Fatal("nested target received dispatch capability")
					}
					if tc.Emit != nil {
						tc.Emit(tool.ProgressInfo{Message: "progress"})
					}
					r := tool.Text("ok")
					r.Extra = []llm.Message{llm.UserText("extra")}
					return r, nil
				}})
				unlock := tool.NewUnlockSet("probe")
				if scenario == "locked" {
					unlock = tool.NewUnlockSet()
				}
				if scenario == "nil_unlock" {
					unlock = nil
				}
				reg := tool.NewRegistry(probe)
				reg.Add(tool.NewExecuteExtraTool(reg, unlock))
				in := QueryInput{Tools: reg, DeferredTools: []string{"probe"}, UnlockSet: unlock, Hooks: h, WorkingDir: "fixture", AgentID: "worker", PermissionMode: permission.ModeBypass, Settlement: &Settlement{DisabledTools: []string{"probe"}}}
				if scenario == "disallowed" {
					in.Disallowed = []string{"probe"}
				}
				if scenario == "plan_mode" {
					in.PermissionMode = permission.ModePlan
				}
				if scenario == "cancelled" {
					cancel()
				}
				name, raw := "probe", json.RawMessage(`{}`)
				if scenario == "bad_input" {
					raw = json.RawMessage(`{"limit":6}`)
				}
				if deferred {
					name = tool.ExecuteExtraToolName
					raw, _ = json.Marshal(map[string]any{"tool_name": "probe", "params": raw})
				}
				l := &loop{ctx: ctx, in: in}
				progress := 0
				result, extra := l.execOne(ctx, scenario == "disabled", llm.ContentBlock{ID: "call-id", Name: name, Input: raw}, func(tool.ProgressInfo) { progress++ })
				allowed := scenario == "allowed" || scenario == "hook_update"
				if result.ToolUseID != "call-id" || result.IsError == allowed {
					t.Fatalf("bad result: %+v", result)
				}
				if allowed {
					if calls != 1 || checks != 1 || len(extra) != 1 || progress != 1 {
						t.Fatalf("calls=%d checks=%d extra=%d progress=%d", calls, checks, len(extra), progress)
					}
					if strings.Join(h.pre, ",") != "probe" || strings.Join(h.post, ",") != "probe" {
						t.Fatalf("audit names: %v %v", h.pre, h.post)
					}
				} else if calls != 0 {
					t.Fatal("denied operation executed")
				}
			})
		}
	}
}

func TestExecutionPanicIsolation(t *testing.T) {
	for _, stage := range []string{"permissions", "readonly", "pre", "run", "post"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			calls := 0
			h := &executionHooks{panicAt: stage}
			probe := tool.Build(tool.Spec{Name: "probe", Schema: map[string]any{"type": "object"}, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
				if stage == "permissions" {
					panic("secret")
				}
				return permission.Allowed()
			}, ReadOnly: func(json.RawMessage) bool {
				if stage == "readonly" {
					panic("secret")
				}
				return false
			}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
				calls++
				if stage == "run" {
					panic("secret")
				}
				return tool.Text("ok"), nil
			}})
			l := &loop{ctx: ctx, in: QueryInput{Tools: tool.NewRegistry(probe), Hooks: h}}
			r, extra := l.execOne(ctx, false, llm.ContentBlock{ID: "panic", Name: "probe", Input: json.RawMessage(`{}`)}, nil)
			b, _ := json.Marshal(r)
			if !r.IsError || r.ToolUseID != "panic" || len(extra) != 0 || strings.Contains(string(b), "secret") || !strings.Contains(string(b), "Do not automatically retry") {
				t.Fatalf("bad panic result %s", b)
			}
			if calls > 1 {
				t.Fatal("operation was retried")
			}
		})
	}
}

func TestPanicDoesNotStallStreamingQueue(t *testing.T) {
	ctx := context.Background()
	done := make(chan struct{}, 1)
	bad := tool.Build(tool.Spec{Name: "bad", Schema: map[string]any{"type": "object"}, Concurrent: func(json.RawMessage) bool { panic("metadata") }, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
		return permission.Allowed()
	}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) { panic("run") }})
	good := tool.Build(tool.Spec{Name: "good", Schema: map[string]any{"type": "object"}, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
		return permission.Allowed()
	}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
		done <- struct{}{}
		return tool.Text("ok"), nil
	}})
	l := &loop{ctx: ctx, toolCtx: ctx, in: QueryInput{Tools: tool.NewRegistry(bad, good)}, sem: make(chan struct{}, 1)}
	e := newStreamExec(l)
	e.add(llm.ContentBlock{ID: "1", Name: "bad", Input: json.RawMessage(`{}`)})
	e.add(llm.ContentBlock{ID: "2", Name: "good", Input: json.RawMessage(`{}`)})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("panic stalled next tool")
	}
}

func TestDeferredCannotReenterOrInvokeOrdinaryTool(t *testing.T) {
	for _, name := range []string{tool.ExecuteExtraToolName, tool.SearchExtraToolsName, "ordinary"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			calls := 0
			probe := tool.Build(tool.Spec{Name: name, Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
				calls++
				return tool.Text("bad"), nil
			}})
			reg := tool.NewRegistry(probe)
			unlock := tool.NewUnlockSet(name)
			reg.Add(tool.NewExecuteExtraTool(reg, unlock))
			l := &loop{ctx: ctx, in: QueryInput{Tools: reg, UnlockSet: unlock}}
			raw, _ := json.Marshal(map[string]any{"tool_name": name, "params": map[string]any{"tool_name": name}})
			r, _ := l.execOne(ctx, false, llm.ContentBlock{ID: "id", Name: tool.ExecuteExtraToolName, Input: raw}, nil)
			if !r.IsError || calls != 0 {
				t.Fatal("nested escalation executed")
			}
		})
	}
}
