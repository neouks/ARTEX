package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const targetAccessRule = "当前结构化可见结果已复核授权，可直接使用；限制状态只适用于列出的 ID，未列出不代表已授权。已知不可用候选本轮跳过，继续已授权方向；未知 asset_ids/node_ids 用 check_target_access 一次批量预检，不逐项查询、不轮询审批、不删资产或替换父节点绕过。全部不可用则结束本轮，不凑意图。历史错误仅记录当时结果，资产和节点 access 元数据已刷新；审批变化后可重新考虑，执行时仍校验最新状态。"

type targetAccessQuery struct {
	AssetIDs []int64 `json:"asset_ids"`
	NodeIDs  []int64 `json:"node_ids"`
}
type targetAssetAccess struct {
	ID    int64  `json:"id"`
	State string `json:"approval_state"`
}
type targetNodeAccess struct {
	ID int64 `json:"id"`
	db.NodeAccess
}
type targetAccessView struct {
	Reasons []string            `json:"reasons,omitempty"`
	Assets  []targetAssetAccess `json:"assets,omitempty"`
	Nodes   []targetNodeAccess  `json:"nodes,omitempty"`
}

func uniqueTargetIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func decodeTargetAccess(raw json.RawMessage) (targetAccessQuery, error) {
	var q targetAccessQuery
	if err := decodeToolInput(raw, &q); err != nil {
		return q, err
	}
	if n := len(q.AssetIDs) + len(q.NodeIDs); n == 0 || n > 50 {
		return q, fmt.Errorf("asset_ids/node_ids 合计须为1..50个正整数 ID")
	}
	for _, ids := range [][]int64{q.AssetIDs, q.NodeIDs} {
		for _, id := range ids {
			if id <= 0 {
				return q, fmt.Errorf("ID 必须为正整数")
			}
		}
	}
	q.AssetIDs = uniqueTargetIDs(q.AssetIDs)
	q.NodeIDs = uniqueTargetIDs(q.NodeIDs)
	return q, nil
}
func queryTargetAccess(store *db.AssetStore, taskID int64, q targetAccessQuery) (targetAccessView, error) {
	var out targetAccessView
	assets, err := store.TaskTargetAssetStates(taskID, q.AssetIDs)
	if err != nil {
		return out, err
	}
	nodes, err := store.TaskNodeAccess(taskID, q.NodeIDs)
	if err != nil {
		return out, err
	}
	for _, id := range q.AssetIDs {
		out.Assets = append(out.Assets, targetAssetAccess{id, assets[id]})
	}
	for _, id := range q.NodeIDs {
		out.Nodes = append(out.Nodes, targetNodeAccess{id, nodes[id]})
	}
	return out, nil
}
func (t *ToolSet) checkTargetAccess() actool.CoreTool {
	ids := func() map[string]any {
		return map[string]any{"type": "array", "maxItems": 50, "items": map[string]any{"type": "integer", "minimum": 1}}
	}
	return readTool("check_target_access", "仅未知候选按需批量查询审批元数据，不返回正文。asset_ids 用于意图资产，node_ids 用于节点读取/父节点授权；父节点仍须为 fact/finding。已复核可用候选不重复查询；不可用候选跳过。合计最多50个 ID。", obj(map[string]any{"asset_ids": ids(), "node_ids": ids()}), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
		ri := RunInfoFrom(ctx)
		if t.as == nil || t.taskID <= 0 || ri.TaskID != t.taskID || (ri.AgentKey != "planner" && ri.AgentKey != "mainagent") {
			return actool.Errorf("check_target_access 需要匹配任务的 Planner/主 Agent 上下文"), nil
		}
		q, err := decodeTargetAccess(raw)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		out, err := queryTargetAccess(t.as.WithReadContext(ctx), t.taskID, q)
		if err != nil {
			return actool.Errorf("审批查询失败: " + err.Error()), nil
		}
		return jsonResult(out)
	})
}

type targetAccessError struct{ Access targetAccessView }

func (e *targetAccessError) Error() string { return "关联目标当前不可用，跳过本项" }
func targetDenied(v targetAccessView) bool {
	for _, a := range v.Assets {
		if a.State != db.ApprovalApproved {
			return true
		}
	}
	for _, n := range v.Nodes {
		if !n.CanRead {
			return true
		}
	}
	return false
}
func accessErrorResult(err error, access any) (actool.Result, error) {
	r, e := jsonResult(map[string]any{"error": err.Error(), "access": access})
	r.IsError = true
	return r, e
}
