package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestTaskAssetManagementToolBoundary(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("management", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	id, err := d.Assets().UpsertRootDomain(db.UpsertRootDomainReq{Domain: "pending-view.test", TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	ts.SetAssetStore(d.Assets(), d.Companies())
	ts.SetTaskID(task.ID)
	ctx := WithRunInfo(context.Background(), RunInfo{TaskID: task.ID, AgentKey: "planner"})
	tool := ts.listTaskAssets()
	result, err := tool.Call(ctx, json.RawMessage(`{"status":"all"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("query: %s %v", result.Flatten(), err)
	}
	var view db.TaskAssetView
	if err := json.Unmarshal([]byte(result.Flatten()), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Assets) != 1 || view.Assets[0].CanSchedule || view.Assets[0].State != "pending" {
		t.Fatalf("pending management: %+v", view)
	}
	for _, ri := range []RunInfo{{TaskID: task.ID, AgentKey: "worker"}, {TaskID: task.ID + 1, AgentKey: "planner"}, {AgentKey: "planner"}} {
		r, err := tool.Call(WithRunInfo(context.Background(), ri), json.RawMessage(`{}`), nil)
		if err != nil || !r.IsError {
			t.Fatal("role/task boundary bypassed")
		}
	}
	for _, input := range []string{`{"task_id":1}`, `{"status":"bad"}`, `null`} {
		r, _ := tool.Call(ctx, json.RawMessage(input), nil)
		if !r.IsError {
			t.Fatal("invalid tool input accepted")
		}
	}
	if _, err := ts.addOneIntent(intentItem{Summary: "test pending", AssetIDs: []json.RawMessage{json.RawMessage(fmt.Sprint(id))}}); err == nil {
		t.Fatal("management ID admitted")
	}
	legacy, err := d.Exploration(task.ExplorationID).AddNode("intent", map[string]any{"summary": "legacy pending"}, 1, "open", "planner", []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := d.Exploration(task.ExplorationID).ClaimIntent(legacy, "worker"); err != nil || claimed {
		t.Fatalf("pending intent claimed: %v %v", claimed, err)
	}
	req := llm.CompletionRequest{Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "view", Name: "list_task_assets", Input: json.RawMessage(`{"status":"all"}`)}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("view", result.Flatten(), false)}}}}
	p := assetContextProvider{assets: d.Assets(), taskID: task.ID}
	read := func(ctx context.Context) string {
		t.Helper()
		fresh, err := p.filter(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return fresh.Messages[1].Content[0].Content[0].Text
	}
	if !strings.Contains(read(ctx), "pending-view.test") {
		t.Fatal("management pending hidden")
	}
	if err := d.Assets().ApproveTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(ctx), `"can_schedule":true`) {
		t.Fatal("history authorization stale")
	}
	if !strings.Contains(req.Messages[1].Content[0].Content[0].Text, `"can_schedule":false`) {
		t.Fatal("audit mutated")
	}
	if err := d.Assets().RevokeTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(ctx), `"approval_state":"revoked"`) {
		t.Fatal("revocation stale")
	}
	if err := d.Assets().BlockTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(ctx), `"approval_state":"blocked"`) {
		t.Fatal("block stale")
	}
	if strings.Contains(read(WithRunInfo(context.Background(), RunInfo{TaskID: task.ID, AgentKey: "worker"})), "pending-view.test") {
		t.Fatal("management history leaked to worker")
	}
	found := false
	for _, seed := range BuiltinToolSeeds() {
		if seed.Key == "list_task_assets" {
			found = true
			if strings.Join(seed.Agents, ",") != "mainagent,planner" {
				t.Fatalf("bindings: %v", seed.Agents)
			}
		}
	}
	if !found {
		t.Fatal("tool not seeded")
	}
}
