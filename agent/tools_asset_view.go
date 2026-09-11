package agent

import (
	"context"
	"encoding/json"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func decodeTaskAssetViewQuery(raw json.RawMessage) (db.TaskAssetViewQuery, error) {
	var q db.TaskAssetViewQuery
	err := decodeToolInput(raw, &q)
	return q, err
}

func (t *ToolSet) listTaskAssets() actool.CoreTool {
	return readTool("list_task_assets", "只读审批管理视图，仅主 Agent/Planner 可用。默认查已批准域名/IP；pending/blocked/revoked 仅供解释，不可下发测试。按需搜索分页，不要每轮拉取全量；summary_only 只取计数。查询不改变授权，详情使用 list_assets。",
		obj(map[string]any{
			"status": map[string]any{"type": "string", "enum": []string{"approved", "pending", "blocked", "revoked", "all"}, "default": "approved"},
			"q":      str("主机名称包含搜索，非 DSL"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20},
			"cursor": str("上次返回的 next_cursor"), "summary_only": map[string]any{"type": "boolean", "default": false},
		}), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			ri := RunInfoFrom(ctx)
			if ri.AgentKey != "planner" && ri.AgentKey != "mainagent" {
				return actool.Errorf("list_task_assets 仅允许主 Agent、Planner 调用"), nil
			}
			if t.as == nil || t.taskID <= 0 || ri.TaskID != t.taskID {
				return actool.Errorf("list_task_assets 需要匹配的任务运行上下文"), nil
			}
			q, err := decodeTaskAssetViewQuery(raw)
			if err != nil {
				return actool.Errorf("参数错误: " + err.Error()), nil
			}
			result, err := t.as.WithReadContext(ctx).QueryTaskAssetView(t.taskID, q)
			if err != nil {
				return actool.Errorf("审批查询失败: " + err.Error()), nil
			}
			return jsonResult(result)
		})
}
