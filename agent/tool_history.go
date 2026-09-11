package agent

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Autumn-27/norma/llm"
)

func cloneToolMessages(req llm.CompletionRequest) llm.CompletionRequest {
	req.Messages = append([]llm.Message(nil), req.Messages...)
	for i := range req.Messages {
		req.Messages[i].Content = append([]llm.ContentBlock(nil), req.Messages[i].Content...)
	}
	return req
}

// Only side-effect-free, structured reads are eligible. Mutation tools (even
// expand_digest / expand_index) and failed calls are deliberately excluded.
func normalizedReadQuery(name string, raw json.RawMessage) string {
	defaults := map[string]any{}
	switch name {
	case "list_facts", "list_findings", "list_worker_traces":
		defaults = map[string]any{"limit": json.Number("20"), "before": json.Number("0"), "q": "", "severity": "", "asset_id": json.Number("0")}
	case "list_task_assets":
		defaults = map[string]any{"status": "approved", "q": "", "limit": json.Number("20"), "cursor": "", "summary_only": false}
	case "list_assets":
		defaults = map[string]any{"limit": json.Number("10"), "offset": json.Number("0"), "detail": false, "type": "", "dsl": "", "field": "", "index": json.Number("0"), "text_offset": json.Number("0")}
	case "node_detail", "get_worker_output":
		defaults = map[string]any{"offset": json.Number("0"), "max_chars": json.Number("8000")}
		if name == "node_detail" {
			defaults["field"] = ""
			defaults["index"] = json.Number("0")
		}
	default:
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var input map[string]any
	if dec.Decode(&input) != nil || input == nil {
		return ""
	}
	for k, v := range input {
		defaults[k] = v
	}
	if q, ok := defaults["q"].(string); ok {
		defaults["q"] = strings.TrimSpace(q)
	}
	// Zero limit is the documented default in the handlers.
	if n, ok := defaults["limit"].(json.Number); ok && n == "0" {
		if name == "list_assets" {
			defaults["limit"] = json.Number("10")
		} else {
			defaults["limit"] = json.Number("20")
		}
	}
	data, err := json.Marshal(defaults)
	if err != nil {
		return ""
	}
	return name + ":" + string(data)
}

func deduplicateToolHistory(req llm.CompletionRequest) llm.CompletionRequest {
	type position struct{ m, b int }
	calls := map[string]string{}
	latest := map[string]position{}
	var replaced []position
	for mi, m := range req.Messages {
		for bi, b := range m.Content {
			if b.Type == llm.BlockToolUse {
				calls[b.ID] = normalizedReadQuery(b.Name, b.Input)
			}
			if b.Type != llm.BlockToolResult || b.IsError {
				continue
			}
			key := calls[b.ToolUseID]
			if key == "" {
				continue
			}
			if old, ok := latest[key]; ok {
				replaced = append(replaced, old)
			}
			latest[key] = position{mi, bi}
		}
	}
	if len(replaced) == 0 {
		return req
	}
	req = cloneToolMessages(req)
	for _, pos := range replaced {
		req.Messages[pos.m].Content[pos.b].Content = []llm.ContentBlock{{Type: llm.BlockText, Text: `{"message":"同一查询页已有后续结果，此历史正文已在模型视图中省略"}`}}
	}
	return req
}
