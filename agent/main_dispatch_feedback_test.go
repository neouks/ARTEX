package agent

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

type dispatchTestProvider struct {
	calls    int
	tool     string
	input    string
	feedback string
	t        *testing.T
}

func (p *dispatchTestProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	for _, tool := range req.Tools {
		if tool.Name == "sleep" {
			p.t.Fatal("main agent exposes sleep")
		}
	}
	if p.feedback != "" && !strings.Contains(strings.Join(req.System, "\n"), p.feedback) {
		p.t.Fatal("restored worker feedback missing")
	}
	if p.tool == "" {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("已读取 Worker 回传结果")}}, "end_turn", llm.Usage{}, nil
	}
	if p.calls > 1 {
		p.t.Fatal("dispatch turn called the model again")
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "dispatch-test", Name: p.tool, Input: []byte(p.input)}}}, "tool_use", llm.Usage{}, nil
}
func (p *dispatchTestProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(y func(llm.StreamEvent, error) bool) {
		m, stop, _, err := p.Complete(ctx, req)
		if err != nil {
			y(llm.StreamEvent{}, err)
			return
		}
		for _, b := range m.Content {
			for _, ev := range []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: b.ID, ToolName: b.Name}, {Type: llm.SEToolInputJSON, Text: string(b.Input)}} {
				if !y(ev, nil) {
					return
				}
			}
		}
		if !y(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop}, nil) {
			return
		}
		y(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func TestMainDispatchEndsTurnWithoutPolling(t *testing.T) {
	for _, tool := range []string{"add_intent", "dispatch_intents"} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%v", tool, stream), func(t *testing.T) {
				d := testDB(t)
				defer d.Close()
				task, err := d.CreateTask("dispatch receipt", "goal", nil, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer d.DeleteTask(task.ID)
				store := d.Exploration(task.ExplorationID)
				store.SetExecutionMode("manual")
				id, err := store.AddIntent(map[string]any{"summary": "selected existing intent"}, 5, nil, "planner")
				if err != nil {
					t.Fatal(err)
				}
				input := `{"summary":"new immediate work"}`
				if tool == "dispatch_intents" {
					input = fmt.Sprintf(`{"intent_ids":[%d]}`, id)
				}
				p := &dispatchTestProvider{tool: tool, input: input, t: t}
				main := NewMainAgent(p, "test", t.TempDir(), nil, 100000, 10)
				main.SetNonStreaming(func() bool { return !stream })
				ctx := WithIntentDispatcher(t.Context(), func(c context.Context, ids []int64) ([]IntentDispatchResult, error) {
					seg, ok := MainDispatchSession(c)
					if !ok || seg != 3 {
						t.Fatal("missing source session", seg, ok)
					}
					var out []IntentDispatchResult
					for _, id := range ids {
						if err := store.MainDispatchFeedback(c, id, seg, true); err != nil {
							return nil, err
						}
						out = append(out, IntentDispatchResult{ID: id, Status: "dispatched"})
					}
					return out, nil
				})
				var acts []db.Activity
				reply, err := main.Chat(ctx, task.ID, 3, nil, nil, store, "goal", "请下发执行", func(a db.Activity) { acts = append(acts, a) }, nil, nil, nil, nil)
				if err != nil || p.calls != 1 || !strings.Contains(reply, "Worker 完成后") {
					t.Fatal(reply, err, p.calls)
				}
				calls, results := 0, 0
				for _, a := range acts {
					if a.Kind == "tool_use" {
						calls++
						if a.Tool != tool {
							t.Fatal("unexpected polling", a.Tool)
						}
					}
					if a.Kind == "tool_result" {
						results++
					}
				}
				if calls != 1 || results != 1 {
					t.Fatal("tool pairing lost", calls, results)
				}
			})
		}
	}
}

func TestDispatchReceiptDoesNotEndRejectedTurn(t *testing.T) {
	ctx := WithMainDispatchSession(t.Context(), 1)
	recordMainDispatch(ctx, []IntentDispatchResult{{ID: 1, Status: "rejected", Error: "blocked"}})
	if got := mainDispatchReceipt(ctx); got != "" {
		t.Fatal(got)
	}
	recordMainDispatch(ctx, []IntentDispatchResult{{ID: 2, Status: "running"}, {ID: 3, Status: "already_dispatched"}})
	recordMainDispatchErrors(ctx, map[string]string{"1": "missing summary"})
	got := mainDispatchReceipt(ctx)
	if !strings.Contains(got, "blocked") || !strings.Contains(got, "#2") || !strings.Contains(got, "#3") || !strings.Contains(got, "第 2 项未登记：missing summary") {
		t.Fatal(got)
	}
	if other := mainDispatchReceipt(WithMainDispatchSession(t.Context(), 2)); other != "" {
		t.Fatal("turn leaked", other)
	}
}

func TestMainUserTurnLoadsPersistedWorkerFeedback(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("feedback context", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	store := d.Exploration(task.ExplorationID)
	id, err := store.AddIntent(map[string]any{"summary": "context test"}, 5, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.MainDispatchFeedback(t.Context(), id, 5, true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AppendActivity(db.Activity{NodeID: &id, Worker: "work#1", Kind: "result", Detail: "durable worker conclusion"}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetIntentState(id, "done"); err != nil {
		t.Fatal(err)
	}
	p := &dispatchTestProvider{t: t, feedback: "durable worker conclusion"}
	main := NewMainAgent(p, "test", t.TempDir(), nil, 100000, 10)
	main.SetNonStreaming(func() bool { return true })
	reply, err := main.Chat(t.Context(), task.ID, 5, nil, nil, d.Exploration(task.ExplorationID), "goal", "刚才 Worker 的结论是什么？", nil, nil, nil, nil, nil)
	if err != nil || !strings.Contains(reply, "已读取 Worker") {
		t.Fatal(reply, err)
	}
}
