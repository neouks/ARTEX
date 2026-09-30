package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

func TestOneShotSessionHeadersAndAttribution(t *testing.T) {
	pg := testDB(t)
	defer pg.Close()
	task, err := pg.CreateTask("one-shot sessions", "fixture goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteTask(task.ID)
	store := pg.Exploration(task.ExplorationID)
	if err := pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"goals", "compactor"} {
		for _, nonStreaming := range []bool{false, true} {
			if role == "compactor" && !nonStreaming {
				continue
			}
			t.Run(fmt.Sprintf("%s/nonStreaming=%t", role, nonStreaming), func(t *testing.T) {
				calls := make(chan string, 4)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls <- r.Header.Get("x-session-id")
					var request struct {
						Stream bool `json:"stream"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.Stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fixture summary\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"fixture summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
					}
				}))
				defer srv.Close()
				provider, err := (Config{Format: llm.FormatOpenAI, BaseURL: srv.URL, Model: "fixture", SessionHeaderKey: "x-session-id"}).NewProvider()
				if err != nil {
					t.Fatal(err)
				}
				profile := fmt.Sprintf("one-shot-%d-%s-%t", task.ID, role, nonStreaming)
				provider = llmrec.Wrap(provider, pg, "fixture", profile, "", "", func() bool { return false })
				ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, ExplorationID: store.ID(), AgentKey: "planner"})
				if role == "goals" {
					DecomposeGoalsWithProvider(ctx, provider, t.TempDir(), "fixture goal", "", nil, store, task.ID, nonStreaming, 0, nil)
				} else {
					ctx = WithRunInfo(ctx, RunInfo{IntentID: 999, RetryOrdinal: 3, Phase: "planner_retry"})
					ctx, err = compactorRunContext(ctx, store)
					if err != nil {
						t.Fatal(err)
					}
					info := RunInfoFrom(ctx)
					if info.IntentID != 0 || info.RetryOrdinal != 0 || info.AgentKey != role || info.TaskID != task.ID || info.ExplorationID != store.ID() {
						t.Fatalf("inherited attribution: %+v", info)
					}
					if _, err := NewCompactor(provider, "").compress(ctx, newColdGraph(nil, nil), block{}, nil); err != nil {
						t.Fatal(err)
					}
				}
				want := fmt.Sprintf("exp%d-%s", store.ID(), role)
				select {
				case got := <-calls:
					if got != want {
						t.Fatalf("session header=%q want=%q", got, want)
					}
				default:
					t.Fatal("no provider request")
				}
				var count int
				if err := pg.QueryRow(`SELECT count(*) FROM llm_usage WHERE profile_name=$1 AND task_id=$2 AND exploration_id=$3 AND worker=$4`, profile, fmt.Sprint(task.ID), store.ID(), role).Scan(&count); err != nil || count != 1 {
					t.Fatalf("usage attribution count=%d err=%v", count, err)
				}
			})
		}
	}
}

func TestCompactorContextPreservesOtherValuesAndRejectsMissingTask(t *testing.T) {
	pg := testDB(t)
	defer pg.Close()
	task, err := pg.CreateTask("detached context", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteTask(task.ID)
	type key struct{}
	parent, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "preserved"))
	ctx, err := compactorRunContext(parent, pg.Exploration(task.ExplorationID))
	if err != nil {
		t.Fatal(err)
	}
	ctx = context.WithoutCancel(ctx)
	cancel()
	if ctx.Err() != nil || ctx.Value(key{}) != "preserved" || transcript.SessionIDFrom(ctx) == "" {
		t.Fatal("detached context lost identity or unrelated values")
	}
	if _, err := compactorRunContext(t.Context(), pg.Exploration(-1)); err == nil {
		t.Fatal("missing task received run identity")
	}
}
