package agent

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

// Revalidate management identities once per model request. Original counts are
// explicitly historical snapshots, not silently refreshed business queries.
func (p assetContextProvider) refreshApprovalManagementHistory(ctx context.Context, req llm.CompletionRequest) (llm.CompletionRequest, error) {
	type item struct {
		m, b int
		view db.TaskAssetView
	}
	var items []item
	names := map[string]string{}
	keys := map[string]bool{}
	ri := RunInfoFrom(ctx)
	allowed := ri.TaskID == p.taskID && (ri.AgentKey == "planner" || ri.AgentKey == "mainagent")
	for mi, m := range req.Messages {
		for bi, b := range m.Content {
			if b.Type == llm.BlockToolUse {
				names[b.ID] = b.Name
			}
			if b.Type != llm.BlockToolResult || names[b.ToolUseID] != "list_task_assets" || b.IsError {
				continue
			}
			for _, c := range b.Content {
				if c.Type != llm.BlockText {
					continue
				}
				var v db.TaskAssetView
				if json.Unmarshal([]byte(c.Text), &v) != nil || v.View != "approval_management" {
					continue
				}
				items = append(items, item{mi, bi, v})
				for _, row := range v.Assets {
					keys[row.GroupKey] = true
				}
			}
		}
	}
	if len(items) == 0 {
		return req, nil
	}
	var current map[string]db.TaskAssetViewRow
	if allowed {
		ids := make([]string, 0, len(keys))
		for k := range keys {
			ids = append(ids, k)
		}
		var err error
		current, err = p.assets.WithReadContext(ctx).RefreshTaskAssetGroups(p.taskID, ids)
		if err != nil {
			return req, err
		}
	}
	req = cloneToolMessages(req)
	for _, it := range items {
		var out any = map[string]any{"view": "approval_management", "message": "该管理视图当前不可用"}
		if allowed {
			rows := []db.TaskAssetViewRow{}
			for _, old := range it.view.Assets {
				if now, ok := current[old.GroupKey]; ok {
					rows = append(rows, now)
				}
			}
			out = map[string]any{"view": "approval_management", "assets": rows, "counts_snapshot": it.view.Counts, "total_snapshot": it.view.Total, "snapshot_note": "计数为历史快照；记录授权已复核，当前计数请查询 list_task_assets"}
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return req, err
		}
		req.Messages[it.m].Content[it.b].Content = []llm.ContentBlock{{Type: llm.BlockText, Text: string(encoded)}}
	}
	return req, nil
}
