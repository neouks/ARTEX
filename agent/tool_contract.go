package agent

import (
	"encoding/json"
	"fmt"

	actool "github.com/Autumn-27/norma/tool"
)

// ResolveBuiltinTool keeps executable structure code-owned. Persisted metadata
// is not rewritten; compatible field descriptions/defaults remain editable.
func ResolveBuiltinTool(t actool.CoreTool, description string, saved map[string]any) actool.CoreTool {
	raw, _ := json.Marshal(t.InputSchema())
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	// Asset selector and pagination instructions are executable contract metadata.
	// Persisted descriptions/defaults may advertise obsolete limit-only calls.
	if t.Name() != "list_assets" && t.Name() != "check_target_access" {
		mergeToolMetadata(schema, saved)
	}
	// These descriptions contain versioned pagination/write protocols. Old saved
	// text must not replace the current contract (e.g. "all findings").
	switch t.Name() {
	case "list_finding_deletion_feedback", "check_target_access", "add_intent", "get_finding_retest_context", "list_facts", "list_findings", "list_task_findings", "list_assets", "node_detail", "get_task_node_detail", "get_worker_output", "get_worker_trace", "list_worker_traces", "search_all_worker_traces", "get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces", "get_task_graph", "add_task_hint", "expand_index", "expand_digest", "record_fact", "report_finding":
		description = t.Description()
	}
	return DecorateTool(t, description, schema)
}

func mergeToolMetadata(code, saved map[string]any) {
	if code == nil || saved == nil {
		return
	}
	if desc, ok := saved["description"].(string); ok {
		code["description"] = desc
	}
	if value, ok := saved["default"]; ok {
		raw, err := json.Marshal(value)
		if err == nil && actool.ValidateInput(code, raw) == nil {
			code["default"] = value
		}
	}
	props, _ := code["properties"].(map[string]any)
	oldProps, _ := saved["properties"].(map[string]any)
	for key, value := range props {
		child, _ := value.(map[string]any)
		old, _ := oldProps[key].(map[string]any)
		mergeToolMetadata(child, old)
	}
	items, _ := code["items"].(map[string]any)
	oldItems, _ := saved["items"].(map[string]any)
	mergeToolMetadata(items, oldItems)
}

func requireRoleTools(role string, tools []actool.CoreTool) error {
	required := map[string][]string{
		"worker":  {"insert_assets", "record_fact", "report_finding"},
		"planner": {"add_intent", "prove_goal"},
	}[role]
	has := make(map[string]bool, len(tools))
	for _, tool := range tools {
		has[tool.Name()] = true
	}
	for _, name := range required {
		if !has[name] {
			return fmt.Errorf("%s 必需工具 %s 已禁用或未绑定，请修正工具配置", role, name)
		}
	}
	return nil
}
