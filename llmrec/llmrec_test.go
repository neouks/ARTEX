package llmrec

import (
	"context"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type completeProvider struct{}

func (completeProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(func(llm.StreamEvent, error) bool) {}
}

func (completeProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			{Type: llm.BlockThinking, Thinking: "reasoning"},
			llm.TextBlock("answer"),
		},
	}, "stop", llm.Usage{InputTokens: 7, OutputTokens: 3}, nil
}

func TestTaskIDContextUsesExplicitRegistryID(t *testing.T) {
	ctx := WithTaskID(context.Background(), " 42 ")
	if got := TaskIDFrom(ctx); got != "42" {
		t.Fatalf("TaskIDFrom()=%q want 42", got)
	}
	if got := TaskIDFrom(WithTaskID(ctx, "   ")); got != "42" {
		t.Fatalf("blank task id should preserve parent context, got %q", got)
	}
	if got := TaskIDFrom(nil); got != "" {
		t.Fatalf("nil context returned %q", got)
	}
}

func TestRunInfoMergesEngineAndAgentAttribution(t *testing.T) {
	ctx := WithRunInfo(context.Background(), RunInfo{Trigger: "heartbeat", RetryOrdinal: 2})
	ctx = WithRunInfo(ctx, RunInfo{TaskID: 42, ExplorationID: 7, IntentID: 9, AgentKey: "worker"})
	got := RunInfoFrom(ctx)
	if got.TaskID != 42 || got.ExplorationID != 7 || got.IntentID != 9 || got.AgentKey != "worker" || got.Trigger != "heartbeat" || got.RetryOrdinal != 2 {
		t.Fatalf("merged run info = %+v", got)
	}
}

func TestRequestDimensions(t *testing.T) {
	req := llm.CompletionRequest{
		System:   []string{"system"},
		Messages: []llm.Message{llm.UserText("hello")},
		Tools:    []llm.ToolSchema{{Name: "read", Description: "read a file", InputSchema: map[string]any{"type": "object"}}},
	}
	dims := requestDimensionsFor(req)
	if dims.system <= 0 || dims.messages <= 0 || dims.tools <= 0 || dims.total != dims.system+dims.messages+dims.tools {
		t.Fatalf("bad request dimensions: %+v", dims)
	}
}

func TestParseSessionFallbackIsExplorationScoped(t *testing.T) {
	taskID, worker := parseSession("exp12-worker-i99")
	if taskID != "12" || worker != "worker" {
		t.Fatalf("parseSession()=(%q,%q)", taskID, worker)
	}
	if taskID, worker := parseSession("not-a-task"); taskID != "" || worker != "" {
		t.Fatalf("unexpected non-session parse: (%q,%q)", taskID, worker)
	}
}

func TestCompleteForwardsAtomicResponse(t *testing.T) {
	recorder := Wrap(completeProvider{}, nil, "model", "profile", "", "", func() bool { return false })
	msg, stopReason, usage, err := recorder.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got := msg.Text(); got != "answer" {
		t.Fatalf("Complete() text=%q want answer", got)
	}
	if stopReason != "stop" {
		t.Fatalf("Complete() stop reason=%q want stop", stopReason)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("Complete() usage=%+v", usage)
	}
}
