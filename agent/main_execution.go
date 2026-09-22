package agent

import (
	"context"
	"encoding/json"
	actool "github.com/Autumn-27/norma/tool"
)

const mainAssetExecutionRule = "主 Agent 可读取、操作未审批 pending 资产，也可明确下发关联意图；这不改变资产审批状态。封禁、撤回、删除、任务隔离和独立动作审批仍生效。历史 pending 拒绝不是当前限制。实际下发以 dispatch 结果为准。"

func (t *ToolSet) enableMainExecution(ctx context.Context) {
	ri := RunInfoFrom(ctx)
	if t.taskID <= 0 || ri.TaskID != t.taskID || ri.AgentKey != "mainagent" || ri.IntentID != 0 {
		return
	}
	t.mainExecution = true
	if t.as != nil {
		t.as = t.as.WithExecutionRead()
	}
	if t.ts != nil {
		t.ts = t.ts.WithWorkerRead()
	}
}

func (t *ToolSet) validatePlanningAssets(ids []int64) error {
	if t.mainExecution {
		return t.as.ValidateWorkerAssets(t.taskID, ids)
	}
	return t.as.ValidateTaskAssetsApproved(t.taskID, ids)
}
func (t *ToolSet) validatePlanningHosts(hosts []string) error {
	if t.mainExecution {
		return t.as.ValidateWorkerHosts(t.taskID, hosts)
	}
	return t.as.ValidateTaskHostsApproved(t.taskID, hosts)
}

// Catalog membership grants no permission: each call builds a request-local
// projection from verified run identity, so catalogs can safely be shared.
type mainRoleTool struct {
	actool.CoreTool
	owner *ToolSet
}

func (tool mainRoleTool) Call(ctx context.Context, raw json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	ri := RunInfoFrom(ctx)
	if ri.AgentKey == "mainagent" && ri.TaskID == tool.owner.taskID && ri.TaskID > 0 && ri.IntentID == 0 {
		copy := *tool.owner
		copy.enableMainExecution(ctx)
		for _, candidate := range copy.mainAgentTools() {
			if candidate.Name() == tool.Name() {
				return candidate.Call(ctx, raw, tc)
			}
		}
	}
	if tool.owner.mainExecution {
		return actool.Errorf("主 Agent 执行上下文与任务不匹配"), nil
	}
	return tool.CoreTool.Call(ctx, raw, tc)
}
