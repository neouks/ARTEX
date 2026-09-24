package agent

import (
	"context"
	"fmt"
	"iter"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Autumn-27/norma/llm"
)

type mainDispatchKey struct{}
type mainDispatchTurn struct {
	mu       sync.Mutex
	seg      int
	results  []IntentDispatchResult
	failures []string
}

// WithMainDispatchSession is host-only context, never an LLM tool argument.
func WithMainDispatchSession(ctx context.Context, seg int) context.Context {
	return context.WithValue(ctx, mainDispatchKey{}, &mainDispatchTurn{seg: seg})
}
func MainDispatchSession(ctx context.Context) (int, bool) {
	t, ok := ctx.Value(mainDispatchKey{}).(*mainDispatchTurn)
	if !ok {
		return 0, false
	}
	return t.seg, true
}
func recordMainDispatch(ctx context.Context, results []IntentDispatchResult) {
	if t, ok := ctx.Value(mainDispatchKey{}).(*mainDispatchTurn); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.results = append(t.results, results...)
	}
}
func recordMainDispatchErrors(ctx context.Context, failures map[string]string) {
	if t, ok := ctx.Value(mainDispatchKey{}).(*mainDispatchTurn); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		keys := make([]string, 0, len(failures))
		for key := range failures {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			index, _ := strconv.Atoi(key)
			t.failures = append(t.failures, fmt.Sprintf("第 %d 项未登记：%s", index+1, failures[key]))
		}
	}
}
func mainDispatchReceipt(ctx context.Context) string {
	t, ok := ctx.Value(mainDispatchKey{}).(*mainDispatchTurn)
	if !ok {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	success := false
	for _, r := range t.results {
		if r.Status != "rejected" {
			success = true
		}
	}
	if !success {
		return ""
	}
	var b strings.Builder
	b.WriteString("下发结果：\n")
	seen := map[int64]bool{}
	for _, r := range t.results {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		status := map[string]string{"dispatched": "已下发", "already_dispatched": "已在执行队列中", "running": "正在执行", "rejected": "未下发"}[r.Status]
		fmt.Fprintf(&b, "- 意图 #%d：%s", r.ID, status)
		if r.Error != "" {
			fmt.Fprintf(&b, "（%s）", r.Error)
		}
		b.WriteString("\n")
	}
	for _, failure := range t.failures {
		fmt.Fprintf(&b, "- %s\n", failure)
	}
	b.WriteString("\nWorker 完成后会将总结返回本会话。你可以继续提问。")
	return b.String()
}

// End a successful dispatch turn locally, preserving normal tool-result pairing
// and transcript capture. No second model request, polling or synthetic tool
// result is needed. The user-visible text is a host-generated dispatch receipt.
type dispatchReceiptProvider struct{ llm.Provider }

func (p dispatchReceiptProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if err := ctx.Err(); err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	if receipt := mainDispatchReceipt(ctx); receipt != "" && len(req.Tools) > 0 {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(receipt)}}, "end_turn", llm.Usage{}, nil
	}
	return p.Provider.Complete(ctx, req)
}
func (p dispatchReceiptProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		if receipt := mainDispatchReceipt(ctx); receipt != "" && len(req.Tools) > 0 {
			for _, ev := range []llm.StreamEvent{{Type: llm.SEMessageStart}, {Type: llm.SETextDelta, Text: receipt}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}, {Type: llm.SEMessageStop}} {
				if !yield(ev, nil) {
					return
				}
			}
			return
		}
		for ev, err := range p.Provider.Stream(ctx, req) {
			if !yield(ev, err) {
				return
			}
		}
	}
}
