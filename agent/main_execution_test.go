package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
	"strings"
	"testing"
)

func TestMainPendingExecutionRoleAndContext(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("main pending", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as, es := d.Assets(), d.Exploration(task.ExplorationID)
	id, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: fmt.Sprintf("main-%d.test", task.ID), TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	node, err := es.AddNode(db.KindFact, map[string]any{"summary": "main-sensitive-evidence"}, 1, "confirmed", "test", []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(es, "human")
	ts.SetTaskID(task.ID)
	ts.SetAssetStore(as, d.Companies())
	tools := map[string]actool.CoreTool{}
	for _, v := range ts.MainAgentTools() {
		tools[v.Name()] = v
	}
	main := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, AgentKey: "mainagent"})
	query := json.RawMessage(fmt.Sprintf(`{"asset_ids":[%d],"node_ids":[%d]}`, id, node))
	r, e := tools["check_target_access"].Call(main, query, nil)
	if e != nil || r.IsError || !strings.Contains(r.Flatten(), `"can_operate":true`) || !strings.Contains(r.Flatten(), `"approval_state":"pending"`) {
		t.Fatal(r, e)
	}
	r, e = tools["node_detail"].Call(main, json.RawMessage(fmt.Sprintf(`{"id":%d}`, node)), nil)
	if e != nil || r.IsError || !strings.Contains(r.Flatten(), "main-sensitive-evidence") {
		t.Fatal(r, e)
	}
	r, e = tools["list_assets"].Call(main, json.RawMessage(fmt.Sprintf(`{"id":%d}`, id)), nil)
	if e != nil || r.IsError || !strings.Contains(r.Flatten(), "pending") {
		t.Fatal(r, e)
	}
	hooks := guard.AssetPolicyHooks(nil, as, task.ID)
	input := []byte(fmt.Sprintf(`{"asset_ids":[%d]}`, id))
	for _, ri := range []RunInfo{{}, {TaskID: task.ID, AgentKey: "planner"}, {TaskID: task.ID + 1, AgentKey: "mainagent"}, {TaskID: task.ID, AgentKey: "mainagent", IntentID: 1}} {
		if blocked, _, _ := hooks.PreToolUse(WithRunInfo(context.Background(), ri), "Bash", input); !blocked {
			t.Fatalf("bad context allowed: %+v", ri)
		}
	}
	if blocked, reason, _ := hooks.PreToolUse(main, "Bash", input); blocked {
		t.Fatal(reason)
	}
	independent := guard.AssetPolicyHooks(workerIndependentRestriction{}, as, task.ID)
	if blocked, why, _ := independent.PreToolUse(main, "Bash", input); !blocked || why != "independent restriction" {
		t.Fatal("independent guard bypassed")
	}
	request := llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "n", Name: "node_detail", Input: json.RawMessage(fmt.Sprintf(`{"id":%d}`, node))}}}, {Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("n", r.Flatten(), false)}}}}
	// Use actual node result, not a list response.
	result, _ := tools["node_detail"].Call(main, json.RawMessage(fmt.Sprintf(`{"id":%d}`, node)), nil)
	request.Messages[1].Content = []llm.ContentBlock{llm.ToolResultText("n", result.Flatten(), false)}
	before, _ := json.Marshal(request)
	filtered, e := FilterTaskAssetRequest(main, as, task.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(filtered)
	if !strings.Contains(string(encoded), "main-sensitive-evidence") {
		t.Fatal("pending main context removed", string(encoded))
	}
	after, _ := json.Marshal(request)
	if string(before) != string(after) {
		t.Fatal("original history mutated")
	}
	planner, e := FilterTaskAssetRequest(WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, AgentKey: "planner"}), as, task.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ = json.Marshal(planner)
	if strings.Contains(string(encoded), "main-sensitive-evidence") {
		t.Fatal("planner read pending body")
	}
	if err := as.RevokeTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if blocked, _, _ := hooks.PreToolUse(main, "Bash", input); !blocked {
		t.Fatal("revoke bypassed")
	}
	r, _ = tools["node_detail"].Call(main, json.RawMessage(fmt.Sprintf(`{"id":%d}`, node)), nil)
	if !r.IsError {
		t.Fatal("revoked node read")
	}
	if err := as.BlockTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if blocked, _, _ := hooks.PreToolUse(main, "Bash", input); !blocked {
		t.Fatal("block bypassed")
	}
}
