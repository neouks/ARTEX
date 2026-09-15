package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

type targetAccessSnapshot struct {
	assets map[int64]string
	nodes  map[int64]db.NodeAccess
}

// Only model-view copies are refreshed. Status identities never include content.
func (p assetContextProvider) refreshTargetAccess(ctx context.Context, req llm.CompletionRequest) (llm.CompletionRequest, *targetAccessSnapshot, error) {
	ri := RunInfoFrom(ctx)
	if ri.TaskID != p.taskID || (ri.AgentKey != "planner" && ri.AgentKey != "mainagent") {
		return req, nil, nil
	}
	system := []string{}
	for _, part := range req.System {
		if !strings.HasPrefix(part, "当前候选审批：") && part != targetAccessRule {
			system = append(system, part)
		}
	}
	req.System = system
	type key struct {
		node bool
		id   int64
	}
	order := []key{}
	seen := map[key]bool{}
	add := func(node bool, id int64) {
		if id > 0 {
			order = append(order, key{node, id})
			seen[key{node, id}] = true
		}
	}
	calls := map[string]llm.ContentBlock{}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse {
				calls[b.ID] = b
				var q struct {
					ID  int64   `json:"id"`
					IDs []int64 `json:"ids"`
					targetAccessQuery
					ParentIDs []int64 `json:"parent_ids"`
					Intents   []struct {
						AssetIDs  []int64 `json:"asset_ids"`
						ParentIDs []int64 `json:"parent_ids"`
					} `json:"intents"`
				}
				if json.Unmarshal(b.Input, &q) != nil {
					continue
				}
				switch b.Name {
				case "check_target_access", "add_intent":
					for _, id := range q.AssetIDs {
						add(false, id)
					}
					for _, id := range q.NodeIDs {
						add(true, id)
					}
					for _, id := range q.ParentIDs {
						add(true, id)
					}
					for _, it := range q.Intents {
						for _, id := range it.AssetIDs {
							add(false, id)
						}
						for _, id := range it.ParentIDs {
							add(true, id)
						}
					}
				case "node_detail":
					add(true, q.ID)
				case "list_assets":
					add(false, q.ID)
					for _, id := range q.IDs {
						add(false, id)
					}
				}
			}
			if b.Type != llm.BlockToolResult {
				continue
			}
			switch calls[b.ToolUseID].Name {
			case "insert_assets", "list_assets", "list_untested_assets", "graph_overview", "node_detail", "expand_digest", "expand_index", "list_findings", "list_facts", "get_worker_output", "get_worker_trace", "list_worker_traces", "search_all_worker_traces":
			default:
				continue
			}
			for _, c := range b.Content {
				if c.Type != llm.BlockText {
					continue
				}
				var value any
				if json.Unmarshal([]byte(c.Text), &value) != nil {
					continue
				}
				assets := map[int64]bool{}
				nodes := map[int64]bool{}
				filterStructuredAssets(value, nil, assets)
				filterStructuredRows(value, nil, nodes, structuredNodeIDs)
				ai := []int64{}
				ni := []int64{}
				for id := range assets {
					ai = append(ai, id)
				}
				for id := range nodes {
					ni = append(ni, id)
				}
				for _, id := range uniqueTargetIDs(ai) {
					add(false, id)
				}
				for _, id := range uniqueTargetIDs(ni) {
					add(true, id)
				}
			}
		}
	}
	if len(order) == 0 {
		return req, nil, nil
	}
	q := targetAccessQuery{}
	for k := range seen {
		if k.node {
			q.NodeIDs = append(q.NodeIDs, k.id)
		} else {
			q.AssetIDs = append(q.AssetIDs, k.id)
		}
	}
	current, err := queryTargetAccess(p.assets.WithReadContext(ctx), p.taskID, q)
	if err != nil {
		return req, nil, err
	}
	assets := map[int64]string{}
	nodes := map[int64]db.NodeAccess{}
	for _, a := range current.Assets {
		assets[a.ID] = a.State
	}
	for _, n := range current.Nodes {
		nodes[n.ID] = n.NodeAccess
	}
	refreshView := func(previous *targetAccessView) {
		for i := range previous.Assets {
			previous.Assets[i].State = assets[previous.Assets[i].ID]
		}
		for i := range previous.Nodes {
			previous.Nodes[i].NodeAccess = nodes[previous.Nodes[i].ID]
		}
	}
	req = cloneToolMessages(req)
	for mi, m := range req.Messages {
		for bi, b := range m.Content {
			name := calls[b.ToolUseID].Name
			if b.Type == llm.BlockToolResult && (name == "node_detail" || name == "add_intent") && len(b.Content) == 1 {
				var result map[string]json.RawMessage
				if json.Unmarshal([]byte(b.Content[0].Text), &result) == nil {
					changed := false
					if raw, ok := result["access"]; ok {
						var v targetAccessView
						if json.Unmarshal(raw, &v) == nil {
							refreshView(&v)
							result["access"], _ = json.Marshal(v)
							changed = true
						}
					}
					if raw, ok := result["access_errors"]; ok {
						var views map[string]targetAccessView
						if json.Unmarshal(raw, &views) == nil {
							for k, v := range views {
								refreshView(&v)
								views[k] = v
							}
							result["access_errors"], _ = json.Marshal(views)
							changed = true
						}
					}
					if changed {
						result["historical_error"] = json.RawMessage("true")
						raw, _ := json.Marshal(result)
						req.Messages[mi].Content[bi].Content = []llm.ContentBlock{{Type: llm.BlockText, Text: string(raw)}}
					}
				}
			}
			if b.Type != llm.BlockToolResult || calls[b.ToolUseID].Name != "check_target_access" || b.IsError {
				continue
			}
			// Do not resurrect results removed by history deduplication.
			var previous targetAccessView
			if len(b.Content) != 1 || json.Unmarshal([]byte(b.Content[0].Text), &previous) != nil || len(previous.Assets)+len(previous.Nodes) == 0 {
				continue
			}
			refreshView(&previous)
			raw, _ := json.Marshal(previous)
			req.Messages[mi].Content[bi].Content = []llm.ContentBlock{{Type: llm.BlockText, Text: string(raw)}}
		}
	}
	restricted := targetAccessView{}
	visited := map[key]bool{}
	total := 0
	for i := len(order) - 1; i >= 0; i-- {
		k := order[i]
		if visited[k] {
			continue
		}
		visited[k] = true
		if k.node && nodes[k.id].CanRead || !k.node && assets[k.id] == db.ApprovalApproved {
			continue
		}
		total++
		if total > 50 {
			continue
		}
		if k.node {
			restricted.Nodes = append(restricted.Nodes, targetNodeAccess{k.id, nodes[k.id]})
		} else {
			restricted.Assets = append(restricted.Assets, targetAssetAccess{k.id, assets[k.id]})
		}
	}
	if total > 0 {
		raw, _ := json.Marshal(map[string]any{"target_access": restricted, "truncated": total > 50})
		req.System = append(append([]string(nil), req.System...), "当前候选审批："+string(raw))
	}
	return req, &targetAccessSnapshot{assets, nodes}, nil
}
