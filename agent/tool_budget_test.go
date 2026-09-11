package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestToolDetailUnicodeAndCollections(t *testing.T) {
	text := strings.Repeat("中文🙂\n", 9000)
	var joined strings.Builder
	for offset := 0; ; {
		out, err := projectDetail(map[string]any{"body": text}, detailWindow{offset, 7999}, "/body", 0)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(out)
		if err != nil || !json.Valid(encoded) {
			t.Fatal("invalid JSON")
		}
		joined.WriteString(out["value"].(string))
		next, more := out["next_offset"].(int)
		if !more {
			break
		}
		if next <= offset {
			t.Fatal("non-progressing cursor")
		}
		offset = next
	}
	if joined.String() != text {
		t.Fatal("text lost or corrupted")
	}
	values := make([]any, 75)
	for i := range values {
		values[i] = strings.Repeat("证据", 100)
	}
	count := 0
	for index := 0; ; {
		out, err := projectDetail(values, detailWindow{0, 800}, "", index)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range out["entries"].([]map[string]any) {
			if row["deferred"] == true {
				detail, err := projectDetail(values, detailWindow{0, 8000}, row["field"].(string), 0)
				if err != nil || detail["value"] != values[count] {
					t.Fatal("deferred evidence unreadable")
				}
			}
			count++
		}
		next, more := out["next_index"].(int)
		if !more {
			break
		}
		if next <= index {
			t.Fatal("non-progressing array")
		}
		index = next
	}
	if count != 75 {
		t.Fatalf("array lost entries: %d", count)
	}
}

func TestNormalizedToolHistoryKeepsPairingAndOriginal(t *testing.T) {
	query := func(id, raw, name string) llm.ContentBlock {
		return llm.ContentBlock{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(raw)}
	}
	result := func(id, text string) llm.ContentBlock {
		return llm.ContentBlock{Type: llm.BlockToolResult, ToolUseID: id, Content: []llm.ContentBlock{{Type: llm.BlockText, Text: text}}}
	}
	req := llm.CompletionRequest{Messages: []llm.Message{{Content: []llm.ContentBlock{
		query("a", `{}`, "list_facts"), result("a", "old"), query("b", `{"q":"","limit":20}`, "list_facts"), result("b", "new"),
		query("c", `{"before":7}`, "list_facts"), result("c", "page two"),
		query("d", `{"id":7}`, "expand_digest"), result("d", "write one"), query("e", `{"id":7}`, "expand_digest"), result("e", "write two"),
	}}}}
	before, _ := json.Marshal(req)
	out := deduplicateToolHistory(req)
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("mutated transcript")
	}
	blocks := out.Messages[0].Content
	if blocks[1].Content[0].Text == "old" || blocks[1].ToolUseID != "a" {
		t.Fatal("dedup/pairing failed")
	}
	for i, want := range map[int]string{3: "new", 5: "page two", 7: "write one", 9: "write two"} {
		if blocks[i].Content[0].Text != want {
			t.Fatalf("incorrectly removed %s", want)
		}
	}
	t.Logf("history before=%d bytes after=%d bytes (tiny fixture is for semantics, not savings)", len(before), len(after))
}

func TestToolInputRejectsInvalidShapes(t *testing.T) {
	for _, input := range []string{`[]`, `null`, `{"limit":"20"}`, `{"unknown":1}`, `{} {}`} {
		var args struct {
			Limit int `json:"limit"`
		}
		if decodeToolInput(json.RawMessage(input), &args) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	for _, window := range []detailWindow{{-1, 20}, {0, -1}, {0, 24001}} {
		if window.validate() == nil {
			t.Fatal("invalid window accepted")
		}
	}
}

func TestToolDefinitionAndHistorySizes(t *testing.T) {
	ts := NewToolSet(nil, "")
	total := 0
	for _, tool := range []interface {
		Name() string
		Description() string
		InputSchema() map[string]any
	}{ts.listAssets(), ts.listFacts(), ts.listFindings(), ts.nodeDetail(), ts.getWorkerOutput(), ts.getWorkerTrace(), ts.expandDigest(), ts.expandIndex(), ts.listWorkerTraces(), ts.searchAllWorkerTraces()} {
		data, err := json.Marshal(map[string]any{"name": tool.Name(), "description": tool.Description(), "parameters": tool.InputSchema()})
		if err != nil {
			t.Fatal(err)
		}
		total += len(data)
		t.Logf("definition %s=%dB", tool.Name(), len(data))
	}
	blocks := []llm.ContentBlock{}
	for _, id := range []string{"one", "two", "three"} {
		blocks = append(blocks,
			llm.ContentBlock{Type: llm.BlockToolUse, ID: id, Name: "list_facts", Input: json.RawMessage(`{}`)},
			llm.ContentBlock{Type: llm.BlockToolResult, ToolUseID: id, Content: []llm.ContentBlock{{Type: llm.BlockText, Text: strings.Repeat("重复事实摘要", 2000)}}},
		)
	}
	req := llm.CompletionRequest{Messages: []llm.Message{{Content: blocks}}}
	before, _ := json.Marshal(req)
	after, _ := json.Marshal(deduplicateToolHistory(req))
	if len(after) >= len(before) {
		t.Fatal("duplicate history not reduced")
	}
	t.Logf("definitions_total=%dB duplicate_history_before=%dB after=%dB; byte counts are not exact Tokens", total, len(before), len(after))
}
