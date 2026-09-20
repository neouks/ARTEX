package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func pairCall(ids ...string) oaMessage {
	m := oaMessage{Role: "assistant", Content: "explanation", ReasoningContent: "reasoning"}
	for _, id := range ids {
		m.ToolCalls = append(m.ToolCalls, oaToolCall{ID: id, Type: "function"})
	}
	return m
}
func pairResult(id string) oaMessage {
	return oaMessage{Role: "tool", ToolCallID: id, Content: "result"}
}
func validOAPairs(messages []oaMessage) error {
	pending := map[string]bool{}
	for _, m := range messages {
		if m.Role == "tool" {
			if !pending[m.ToolCallID] {
				return fmt.Errorf("unexpected result")
			}
			delete(pending, m.ToolCallID)
			continue
		}
		if len(pending) > 0 {
			return fmt.Errorf("missing result before %s", m.Role)
		}
		for _, c := range m.ToolCalls {
			if c.ID == "" || pending[c.ID] {
				return fmt.Errorf("invalid call ID")
			}
			pending[c.ID] = true
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("missing trailing result")
	}
	return nil
}
func TestOpenAIPairRepair(t *testing.T) {
	for _, tc := range []struct {
		name           string
		in             []oaMessage
		calls, results int
	}{
		{"single", []oaMessage{pairCall("a"), pairResult("a")}, 1, 1},
		{"parallel reversed", []oaMessage{pairCall("a", "b"), pairResult("b"), pairResult("a")}, 2, 2},
		{"partial", []oaMessage{pairCall("a", "b"), pairResult("a")}, 1, 1},
		{"cancelled", []oaMessage{pairCall("a")}, 0, 0},
		{"summary boundary", []oaMessage{pairCall("a"), {Role: "user", Content: "summary"}, pairResult("a")}, 0, 0},
		{"assistant boundary", []oaMessage{pairCall("a"), {Role: "assistant", Content: "text"}, pairResult("a")}, 0, 0},
		{"system boundary", []oaMessage{pairCall("a"), {Role: "system", Content: "rule"}, pairResult("a")}, 0, 0},
		{"orphan", []oaMessage{pairResult("a")}, 0, 0},
		{"duplicates", []oaMessage{pairCall("a", "a", ""), pairResult("a"), pairResult("a"), pairResult("")}, 1, 1},
		{"reused across turns", []oaMessage{pairCall("a"), pairResult("a"), pairCall("a"), pairResult("a")}, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.in)
			out := pairOpenAIMessages(tc.in)
			if err := validOAPairs(out); err != nil {
				t.Fatal(err)
			}
			calls, results := 0, 0
			for _, m := range out {
				calls += len(m.ToolCalls)
				if m.Role == "tool" {
					results++
				}
				if m.Role == "assistant" && m.ReasoningContent != "reasoning" && m.Content != "text" {
					t.Fatal("lost reasoning")
				}
			}
			if calls != tc.calls || results != tc.results {
				t.Fatalf("counts %d %d", calls, results)
			}
			after, _ := json.Marshal(tc.in)
			if string(before) != string(after) {
				t.Fatal("mutated input")
			}
			if err := validOAPairs(tc.in); err == nil && !reflect.DeepEqual(tc.in, out) {
				t.Fatal("changed valid history")
			}
			if !reflect.DeepEqual(out, pairOpenAIMessages(out)) {
				t.Fatal("not idempotent")
			}
		})
	}
}

func TestOpenAIPairRepairAtTransport(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var req oaReq
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if err := validOAPairs(req.Messages); err != nil {
					t.Error(err)
					http.Error(w, "invalid pairing", 400)
					return
				}
				if req.Stream != stream {
					t.Error("wrong transport")
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				}
			}))
			defer srv.Close()
			p, err := NewProvider(Config{Format: FormatOpenAI, BaseURL: srv.URL, Model: "test", APIKey: "test"})
			if err != nil {
				t.Fatal(err)
			}
			req := CompletionRequest{Messages: []Message{
				{Role: RoleAssistant, Content: []ContentBlock{{Type: BlockToolUse, ID: "a", Name: "Compress", Input: json.RawMessage(`{}`)}, {Type: BlockToolUse, ID: "b", Name: "Read", Input: json.RawMessage(`{}`)}}},
				{Role: RoleUser, Content: []ContentBlock{{Type: BlockToolResult, ToolUseID: "a", Content: []ContentBlock{TextBlock("done")}}, TextBlock("summary")}},
				{Role: RoleUser, Content: []ContentBlock{{Type: BlockToolResult, ToolUseID: "b", Content: []ContentBlock{TextBlock("late")}}}},
				UserText("continue"),
			}}
			before, _ := json.Marshal(req)
			if stream {
				for _, err := range p.Stream(context.Background(), req) {
					if err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if _, _, _, err := p.Complete(context.Background(), req); err != nil {
					t.Fatal(err)
				}
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("mutated history")
			}
			if requests.Load() != 1 {
				t.Fatalf("unexpected retries: %d", requests.Load())
			}
		})
	}
}
