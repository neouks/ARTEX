package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

func assetStructuredTool(name string) bool {
	switch name {
	case "insert_assets", "list_assets", "list_untested_assets", "graph_overview", "node_detail", "expand_digest", "expand_index", "list_findings", "list_facts", "get_worker_output", "get_worker_trace", "list_worker_traces", "search_all_worker_traces":
		return true
	}
	return false
}

// Deferred dispatch has the target's result and invocation id, but its outer
// tool_use names the access wrapper. Revalidation must use the actual target.
func assetContextCall(call llm.ContentBlock) llm.ContentBlock {
	if call.Name == actool.ExecuteExtraToolName {
		var in struct {
			Name   string          `json:"tool_name"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(call.Input, &in) == nil && in.Name != actool.ExecuteExtraToolName {
			call.Name, call.Input = in.Name, in.Params
		}
	}
	return call
}

// Structured domain output must retain machine-readable authorization identity.
// A raw head/tail slice of JSON would silently bypass the next request's local
// approval refresh. Small results keep their original success protocol.
func captureStructuredResult(name string, result actool.Result, tc *actool.ToolContext) actool.Result {
	if tc == nil || !assetStructuredTool(name) {
		return result
	}
	max := tc.MaxOutputChars
	if max <= 0 {
		max = 30000
	}
	result.Content = slices.Clone(result.Content)
	for i, content := range result.Content {
		if content.Type != llm.BlockText || len(content.Text) <= max {
			continue
		}
		var value any
		if json.Unmarshal([]byte(content.Text), &value) != nil {
			continue
		}
		assets, nodes := map[int64]bool{}, map[int64]bool{}
		filterStructuredAssets(value, nil, assets)
		filterStructuredRows(value, nil, nodes, structuredNodeIDs)
		ids := func(set map[int64]bool) []int64 {
			out := make([]int64, 0, len(set))
			for id := range set {
				out = append(out, id)
			}
			slices.Sort(out)
			return out
		}
		captured := actool.Capture(tc, content.Text)
		head, footer := captured, ""
		if at := strings.LastIndex(captured, "\n\n... <persisted-output>"); at >= 0 {
			head, footer = captured[:at], captured[at:]
		}
		envelope := map[string]any{"output_truncated": true, "asset_ids": ids(assets), "node_ids": ids(nodes), "preview": footer, "message": "预览为历史快照；请缩小查询范围或分页读取"}
		encoded, _ := json.Marshal(envelope)
		if len(encoded) > max {
			// Never retain a sensitive preview or source pointer while dropping
			// some of the ids required to revalidate it. Ask for a smaller query.
			encoded = []byte(`{"output_truncated":true,"message":"请缩小查询范围或分页读取"}`)
			if len(encoded) > max {
				encoded = []byte(`{}`)
			}
		} else {
			low, high := 0, len(head)
			for low <= high {
				mid := low + (high-low)/2
				end := mid
				for end > 0 && end < len(head) && !utf8.RuneStart(head[end]) {
					end--
				}
				envelope["preview"] = head[:end] + footer
				candidate, _ := json.Marshal(envelope)
				if len(candidate) <= max {
					encoded, low = candidate, mid+1
				} else {
					high = mid - 1
				}
			}
		}
		result.Content[i].Text = string(encoded)
	}
	return result
}
