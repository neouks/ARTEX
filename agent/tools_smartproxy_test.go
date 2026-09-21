package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// stubSmart records marks so a test can assert the tools reached a real
// (stubbed) selector instead of failing with "未启用".
type stubSmart struct {
	marks map[string]string
}

func newStubSmart() *stubSmart { return &stubSmart{marks: map[string]string{}} }

func (s *stubSmart) Mark(_ int64, host, reason, _, _ string) {
	s.marks[strings.ToLower(host)] = reason
}

func (s *stubSmart) Marked(_ int64, host string) bool {
	_, ok := s.marks[strings.ToLower(host)]
	return ok
}

// ctxWithTask builds a run context carrying a task id, which the tools require.
func ctxWithTask(taskID int64) context.Context {
	return WithRunInfo(context.Background(), RunInfo{TaskID: taskID, AgentKey: "planner"})
}

// TestSmartProxyToolsRequireInjection pins the exact bug that broke a real run:
// the tools were registered in PlannerTools/MainAgentTools but only the worker's
// ToolSet had a selector injected, so the planner saw "智能代理未启用".
func TestSmartProxyToolsRequireInjection(t *testing.T) {
	// Without injection: refuse cleanly (no panic, clear reason).
	ts := NewToolSet(nil, "planner")
	res, err := ts.markHostProxy().Call(ctxWithTask(7), json.RawMessage(`{"host":"a.example","reason":"r"}`), nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Flatten(), "未启用") {
		t.Fatalf("uninjected tool must report 未启用, got %q", res.Flatten())
	}

	// With injection: the mark lands.
	stub := newStubSmart()
	ts.SetSmartProxy(stub)
	res2, err := ts.markHostProxy().Call(ctxWithTask(7), json.RawMessage(`{"host":"A.Example","reason":"waf"}`), nil)
	if err != nil {
		t.Fatalf("mark call failed: %v", err)
	}
	if res2.IsError {
		t.Fatalf("mark rejected: %s", res2.Flatten())
	}
	if !stub.Marked(7, "a.example") {
		t.Fatal("mark did not reach the selector")
	}

	// check_host_proxy must observe it.
	res3, err := ts.checkHostProxy().Call(ctxWithTask(7), json.RawMessage(`{"host":"a.example"}`), nil)
	if err != nil {
		t.Fatalf("check call failed: %v", err)
	}
	if !strings.Contains(res3.Flatten(), `"marked":true`) {
		t.Fatalf("check did not report marked: %s", res3.Flatten())
	}
}

// TestSmartProxyToolsRegisteredOnAllSurfaces guards the registration side of the
// same defect: a surface exposing the tools must be able to inject a selector.
func TestSmartProxyToolsRegisteredOnAllSurfaces(t *testing.T) {
	ts := ToolSet{}
	collect := func(surface string, tools []actool.CoreTool) {
		names := map[string]bool{}
		for _, tl := range tools {
			names[tl.Name()] = true
		}
		if !names["mark_host_proxy"] || !names["check_host_proxy"] {
			t.Fatalf("%s surface must expose both smart-proxy tools, got %v", surface, names)
		}
	}
	collect("planner", ts.PlannerTools())
	collect("main-agent", ts.MainAgentTools())
}

// TestSmartProxyToolRequiresTaskContext makes sure the tool cannot be used
// outside a task, where a mark would have no scope to belong to.
func TestSmartProxyToolRequiresTaskContext(t *testing.T) {
	ts := NewToolSet(nil, "planner")
	ts.SetSmartProxy(newStubSmart())
	res, err := ts.markHostProxy().Call(context.Background(), json.RawMessage(`{"host":"a.example","reason":"r"}`), nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Flatten(), "任务上下文") {
		t.Fatalf("expected a task-context error, got %q", res.Flatten())
	}
}

// TestSmartProxyToolRejectsEmptyArgs keeps the tool from recording meaningless
// marks, which would silently route a host through the pool with no rationale.
func TestSmartProxyToolRejectsEmptyArgs(t *testing.T) {
	ts := NewToolSet(nil, "planner")
	ts.SetSmartProxy(newStubSmart())
	res, err := ts.markHostProxy().Call(ctxWithTask(1), json.RawMessage(`{"host":"","reason":""}`), nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatal("empty host/reason must be rejected")
	}
}

// TestSmartProxyRuleMentionsNonBlockingCases guards the prompt contract: the
// agent must be told what is NOT a block, otherwise it marks business 403s.
func TestSmartProxyRuleMentionsNonBlockingCases(t *testing.T) {
	for _, want := range []string{"WWW-Authenticate", "401", "不算被拦截"} {
		if !strings.Contains(smartProxyRule, want) {
			t.Fatalf("smartProxyRule must spell out %q to prevent false marks", want)
		}
	}
}

// TestSmartProxyFallsBackToGlobal pins the fix for the defect that broke real
// runs three times: the tools are registered on several agent surfaces, but the
// selector had to be wired into each ToolSet construction site, and every missed
// site produced "智能代理未启用" for that whole agent kind.
//
// The process-wide default must therefore make an UNWIRED ToolSet work, so a
// future surface cannot silently break the tools by forgetting the injection.
func TestSmartProxyFallsBackToGlobal(t *testing.T) {
	stub := newStubSmart()
	prev := globalSmartProxy
	SetGlobalSmartProxy(stub)
	t.Cleanup(func() { SetGlobalSmartProxy(prev) })

	// Deliberately do NOT call SetSmartProxy on this ToolSet.
	ts := NewToolSet(nil, "orchestrator")
	res, err := ts.markHostProxy().Call(ctxWithTask(11), json.RawMessage(`{"host":"b.example","reason":"waf"}`), nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unwired ToolSet must still work via the global default: %s", res.Flatten())
	}
	if !stub.Marked(11, "b.example") {
		t.Fatal("global default did not receive the mark")
	}

	// An explicit per-set selector still takes precedence.
	own := newStubSmart()
	ts.SetSmartProxy(own)
	if _, err := ts.markHostProxy().Call(ctxWithTask(12), json.RawMessage(`{"host":"c.example","reason":"waf"}`), nil); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if !own.Marked(12, "c.example") || stub.Marked(12, "c.example") {
		t.Fatal("per-set selector must win over the global default")
	}
}
