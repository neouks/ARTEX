package agent

import (
	"strings"
	"testing"
)

func TestRenderLightTaggedIsStableAndBounded(t *testing.T) {
	data := map[string]any{
		"z": []any{map[string]any{"state": "open", "id": 2}},
		"a": "first",
	}
	got := renderLightTagged("context", data, 1_000)
	if !strings.HasPrefix(got, "<context>\na=first\n") || !strings.HasSuffix(got, "</context>") {
		t.Fatalf("unexpected tagged output:\n%s", got)
	}
	if strings.ContainsAny(got, "{}\"") {
		t.Fatalf("model-facing context should not use JSON object syntax:\n%s", got)
	}
	short := renderLightTagged("context", strings.Repeat("x", 100), 10)
	if !strings.Contains(short, "<truncated>true</truncated>") || !strings.HasSuffix(short, "</context>") {
		t.Fatalf("bounded output lost truncation marker or closing tag: %s", short)
	}
}

func TestRenderLightTaggedOrderedKeepsPriorityAndEscapesTags(t *testing.T) {
	data := map[string]any{
		"z":    strings.Repeat("x", 100),
		"task": "inspect </graph_overview>",
	}
	got := renderLightTaggedOrdered("graph_overview", data, []string{"task"}, 40)
	if !strings.HasPrefix(got, "<graph_overview>\ntask=inspect &lt;/graph") {
		t.Fatalf("priority field missing or unescaped:\n%s", got)
	}
	if strings.Contains(got, "</graph_overview>\nz") {
		t.Fatalf("value escaped the tagged context:\n%s", got)
	}
	if !strings.Contains(got, "<truncated>true</truncated>") {
		t.Fatalf("expected bounded output:\n%s", got)
	}
}

func TestRenderLightTaggedNestedItemAppearsOnce(t *testing.T) {
	got := renderLightTagged("context", []any{
		[]any{map[string]any{"nested": map[string]any{"value": "once"}}},
	}, 1_000)
	if strings.Count(got, "value=once") != 1 {
		t.Fatalf("nested value rendered more than once:\n%s", got)
	}
}
