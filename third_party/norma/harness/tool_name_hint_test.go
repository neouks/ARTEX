package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

func TestToolNameHint(t *testing.T) {
	schemas := []llm.ToolSchema{{Name: "record_fact"}, {Name: "report_finding"}, {Name: "list_assets"}}
	for wrong, correct := range map[string]string{"RecordFact": "record_fact", "record_facts": "record_fact", "record_finding": "report_finding", "ListAssets": "list_assets"} {
		if got := toolNameHint(wrong, schemas); !strings.Contains(got, correct) || !strings.Contains(got, "未执行") {
			t.Fatalf("%s: %s", wrong, got)
		}
	}
	if got := toolNameHint("RecordFact", nil); got != "" {
		t.Fatal("invented unavailable tool")
	}
	if got := toolNameHint(strings.Repeat("x", 5000), schemas); got != "" {
		t.Fatal("unbounded name")
	}
	filtered := filterToolSchemas(schemas, []string{"record_fact"})
	if got := toolNameHint("RecordFact", filtered); strings.Contains(got, "record_fact") {
		t.Fatal("disabled/deferred name disclosed")
	}
}

func TestUnknownToolHintNeverDispatches(t *testing.T) {
	called := false
	registered := tool.Build(tool.Spec{Name: "report_finding", Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
		called = true
		return tool.Text("written"), nil
	}})
	l := &loop{in: QueryInput{Tools: tool.NewRegistry(registered)}}
	result, _ := l.execOne(context.Background(), false, llm.ContentBlock{ID: "call-1", Name: "record_finding", Input: json.RawMessage(`{"summary":"placeholder"}`)}, nil)
	if called || !result.IsError || result.ToolUseID != "call-1" {
		t.Fatal("unknown call executed or result pairing lost")
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "report_finding") {
		t.Fatal("missing correction hint")
	}
	l.in.DeferredTools = []string{"report_finding"}
	result, _ = l.execOne(context.Background(), false, llm.ContentBlock{ID: "call-2", Name: "record_finding"}, nil)
	raw, _ = json.Marshal(result)
	if strings.Contains(string(raw), "report_finding") {
		t.Fatal("deferred tool exposed")
	}
}

func TestStreamExecutorKeepsOriginalToolContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &loop{toolCtx: ctx}
	old := newStreamExec(l)
	cancel()
	l.toolCtx, l.settling = context.Background(), true
	current := newStreamExec(l)
	if old.toolCtx.Err() == nil || old.settling {
		t.Fatal("old executor inherited settlement context")
	}
	if current.toolCtx.Err() != nil || !current.settling {
		t.Fatal("settlement context not captured")
	}
}
