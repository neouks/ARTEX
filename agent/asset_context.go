package agent

import (
	"context"
	"encoding/json"
	"iter"
	"net"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func (t *ToolSet) authorizedScope(rows []db.TaskScope) []db.TaskScope {
	if t.as == nil || t.taskID <= 0 {
		return rows
	}
	hosts := make([]string, 0, len(rows))
	hostOf := func(row db.TaskScope) string {
		if row.Domain != "" {
			return db.DomainKey(row.Domain)
		}
		if ip, _, err := net.ParseCIDR(row.Net); err == nil {
			return ip.String()
		}
		return ""
	}
	for _, row := range rows {
		if host := hostOf(row); host != "" {
			hosts = append(hosts, host)
		}
	}
	states, err := t.as.TaskHostApprovalStates(t.taskID, hosts)
	if err != nil {
		return nil
	}
	out := make([]db.TaskScope, 0, len(rows))
	for _, row := range rows {
		if host := hostOf(row); host != "" && states[host] != db.ApprovalApproved {
			continue
		}
		out = append(out, row)
	}
	return out
}

// Revalidate persisted structured tool results immediately before every model
// call, including session resume and compaction. Never rewrite stored audit data.
type assetContextProvider struct {
	llm.Provider
	assets *db.AssetStore
	taskID int64
}

func withAssetContext(provider llm.Provider, assets *db.AssetStore, taskID int64) llm.Provider {
	if assets == nil || taskID <= 0 {
		return provider
	}
	return assetContextProvider{Provider: provider, assets: assets, taskID: taskID}
}

func (p assetContextProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	filtered, err := p.filter(ctx, req)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	return p.Provider.Complete(ctx, filtered)
}

func (p assetContextProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		filtered, err := p.filter(ctx, req)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		for event, err := range p.Provider.Stream(ctx, filtered) {
			if !yield(event, err) {
				return
			}
		}
	}
}

func structuredAssetIDs(row map[string]any) []int64 {
	var ids []int64
	appendID := func(value any) {
		if id, ok := value.(float64); ok && id > 0 && id == float64(int64(id)) {
			ids = append(ids, int64(id))
		}
	}
	appendID(row["asset_id"])
	if values, ok := row["asset_ids"].([]any); ok {
		for _, value := range values {
			appendID(value)
		}
	}
	switch row["type"] {
	case "root_domain", "subdomain", "ip", "service", "endpoint", "app":
		appendID(row["id"])
	}
	return ids
}

func filterStructuredAssets(value any, states map[int64]string, collect map[int64]bool) (any, bool) {
	return filterStructuredRows(value, states, collect, structuredAssetIDs)
}

func structuredNodeIDs(row map[string]any) []int64 {
	if _, asset := row["type"]; asset {
		return nil
	}
	_, summary := row["summary"]
	_, state := row["state"]
	_, kind := row["kind"]
	if id, ok := row["id"].(float64); ok && id > 0 && (summary || (state && kind)) {
		return []int64{int64(id)}
	}
	return nil
}

func filterStructuredRows(value any, states map[int64]string, collect map[int64]bool, rowIDs func(map[string]any) []int64) (any, bool) {
	switch row := value.(type) {
	case map[string]any:
		for _, id := range rowIDs(row) {
			if collect != nil {
				collect[id] = true
			} else if states[id] != db.ApprovalApproved {
				return nil, false
			}
		}
		for key, child := range row {
			filtered, keep := filterStructuredRows(child, states, collect, rowIDs)
			if collect == nil {
				if keep {
					row[key] = filtered
				} else {
					delete(row, key)
				}
			}
		}
	case []any:
		out := make([]any, 0, len(row))
		for _, child := range row {
			if filtered, keep := filterStructuredRows(child, states, collect, rowIDs); keep {
				out = append(out, filtered)
			}
		}
		return out, true
	}
	return value, true
}

func (p assetContextProvider) filter(ctx context.Context, req llm.CompletionRequest) (llm.CompletionRequest, error) {
	template, err := p.assets.WithReadContext(ctx).TaskApprovalTemplate(p.taskID)
	if err != nil {
		return req, err
	}
	skips, err := p.assets.WithReadContext(ctx).ActiveTaskAssetSkips(p.taskID)
	if err != nil {
		return req, err
	}
	if message := db.TaskAssetSkipMessage(skips); message != "" {
		req.System = append(append([]string(nil), req.System...), message)
	} else {
		req.System = append(append([]string(nil), req.System...), "当前跳过清单为空。历史拦截结果不是永久禁令；始终以最新资产授权和工具校验为准。")
	}
	policy := "仅用户明确提供或人工批准的域名/IP可测试；其他发现登记后等待用户审批。"
	switch template {
	case "all_assets":
		policy = "所有合法新发现域名/IP由系统自动批准，可直接测试，无需申请审批。"
	case "related_assets":
		policy = "用户目标同根域的子域名及有本任务DNS解析依据的IP由系统自动批准，可直接测试；其他发现登记后等待用户审批。"
	}
	req.System = append(append([]string(nil), req.System...), "资产模板："+policy+"审批仅作用于域名/IP，获准主机的所有端口、服务和接口无需单独审批。用户封禁、撤回及删除限制优先；登记结果未返回的候选项不要测试或反复重试，批准后系统会重新调度。")
	type result struct {
		message, block, content int
		value                   any
	}
	var results []result
	ids := make(map[int64]bool)
	nodeIDs := make(map[int64]bool)
	toolNames := make(map[string]string)
	for mi, message := range req.Messages {
		for bi, block := range message.Content {
			if block.Type == llm.BlockToolUse {
				toolNames[block.ID] = block.Name
			}
			if block.Type != llm.BlockToolResult {
				continue
			}
			// Only ARTEX structured tools: arbitrary shell/HTTP JSON may use the
			// same field names for unrelated application data.
			switch toolNames[block.ToolUseID] {
			case "insert_assets", "list_assets", "list_untested_assets", "graph_overview", "node_detail", "expand_digest", "list_findings", "list_facts":
			default:
				continue
			}
			for ci, content := range block.Content {
				var value any
				if content.Type != llm.BlockText || json.Unmarshal([]byte(content.Text), &value) != nil {
					continue
				}
				filterStructuredAssets(value, nil, ids)
				filterStructuredRows(value, nil, nodeIDs, structuredNodeIDs)
				results = append(results, result{mi, bi, ci, value})
			}
		}
	}
	if len(ids) == 0 && len(nodeIDs) == 0 {
		return req, nil
	}
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	states, err := p.assets.WithReadContext(ctx).TaskAssetApprovalStates(p.taskID, list)
	if err != nil {
		return req, err
	}
	list = list[:0]
	for id := range nodeIDs {
		list = append(list, id)
	}
	nodeStates, err := p.assets.WithReadContext(ctx).TaskNodeApprovalStates(p.taskID, list)
	if err != nil {
		return req, err
	}
	req.Messages = append([]llm.Message(nil), req.Messages...)
	for i := range req.Messages {
		req.Messages[i].Content = append([]llm.ContentBlock(nil), req.Messages[i].Content...)
	}
	for _, r := range results {
		value, keep := filterStructuredAssets(r.value, states, nil)
		if keep {
			value, keep = filterStructuredRows(value, nodeStates, nil, structuredNodeIDs)
		}
		if !keep {
			value = map[string]any{"message": "该结果已从可执行上下文移除，等待用户授权"}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return req, err
		}
		block := &req.Messages[r.message].Content[r.block]
		block.Content = append([]llm.ContentBlock(nil), block.Content...)
		block.Content[r.content].Text = string(encoded)
	}
	return req, nil
}
