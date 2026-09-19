package harness

import (
	"encoding/json"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

// dedupeDiscoveryResults operates after context projection, so a reference can
// only replace an identical successful result still present in this request.
// Copy-on-write preserves transcripts, compaction state, and audit records.
func dedupeDiscoveryResults(msgs []llm.Message) []llm.Message {
	discovery := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse && b.Name == tool.SearchExtraToolsName {
				discovery[b.ID] = true
			}
		}
	}
	const omitted = "Duplicate tool discovery omitted; use the identical later discovery result in this conversation."
	seen := map[string]bool{}
	out := append([]llm.Message(nil), msgs...)
	for i := len(out) - 1; i >= 0; i-- {
		copied := false
		for j := len(out[i].Content) - 1; j >= 0; j-- {
			b := out[i].Content[j]
			if b.Type != llm.BlockToolResult || b.IsError || !discovery[b.ToolUseID] {
				continue
			}
			raw, err := json.Marshal(b.Content)
			if err != nil {
				continue
			}
			if len(raw) <= len(omitted)+32 {
				continue // Replacing a short status must not increase input size.
			}
			key := string(raw)
			if !seen[key] {
				seen[key] = true
				continue
			}
			if !copied {
				out[i].Content = append([]llm.ContentBlock(nil), out[i].Content...)
				copied = true
			}
			out[i].Content[j].Content = []llm.ContentBlock{llm.TextBlock(omitted)}
		}
	}
	return out
}
