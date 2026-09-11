package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

func (t *ToolSet) registerDescriptionAsset() actool.CoreTool {
	return writeTool("register_user_target", "登记用户明确提供的测试资产。仅目标分解阶段可用；evidence 必须逐字引用描述或目标中的授权语句，不得把禁止项、参考地址或变量作为目标。域名精确登记，通配域名须保留 *. 前缀。",
		obj(map[string]any{"kind": str("host 或 cidr"), "value": str("原文明示的主机/IP/CIDR/通配域名；URL取完整主机名"), "evidence": str("逐字引用包含目标的完整授权语句")}, "kind", "value", "evidence"),
		func(_ context.Context, raw json.RawMessage) (actool.Result, error) {
			var in struct{ Kind, Value, Evidence string }
			if err := json.Unmarshal(raw, &in); err != nil {
				return actool.Errorf("参数无效：" + err.Error()), nil
			}
			if in.Kind != "host" && in.Kind != "cidr" {
				return actool.Errorf("kind 必须是 host 或 cidr"), nil
			}
			id, err := t.as.RegisterDescriptionAsset(t.taskID, in.Kind, in.Value, in.Evidence)
			if err != nil {
				return actool.Errorf("用户目标登记失败：" + err.Error()), nil
			}
			return jsonResult(map[string]any{"asset_id": id, "source": "用户提供"})
		})
}
