package harness

import (
	"encoding/json"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
	"strings"
	"testing"
)

func TestDiscoveryProjectionPreservesAudit(t *testing.T) {
	var msgs []llm.Message
	for _, id := range []string{"a", "b", "c"} {
		msgs = append(msgs, llm.Message{Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: id, Name: tool.SearchExtraToolsName}}}, llm.Message{Content: []llm.ContentBlock{llm.ToolResultText(id, strings.Repeat("schema ", 1000), false)}})
	}
	before, _ := json.Marshal(msgs)
	out := dedupeDiscoveryResults(msgs)
	after, _ := json.Marshal(msgs)
	if string(before) != string(after) {
		t.Fatal("audit mutated")
	}
	projected, _ := json.Marshal(out)
	if len(projected) >= len(before)/2 {
		t.Fatal("duplicate schemas retained")
	}
	t.Logf("input bytes: %d -> %d", len(before), len(projected))
	if out[5].Content[0].Content[0].Text != msgs[5].Content[0].Content[0].Text {
		t.Fatal("latest lost")
	}
	// Once the later occurrence is outside the view, the earlier one stays full.
	single := dedupeDiscoveryResults(msgs[:2])
	if single[1].Content[0].Content[0].Text != msgs[1].Content[0].Content[0].Text {
		t.Fatal("dangling reference")
	}
}
