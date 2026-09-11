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

func TestPendingDiscoveryIsNotReturnedOrScheduled(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("approval queue", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := NewToolSet(d.Exploration(task.ExplorationID), "worker")
	ts.SetAssetStore(d.Assets(), d.Companies())
	ts.SetTaskID(task.ID)
	value := callReadJSON(t, ts.insertAssets(), `{"assets":[{"type":"subdomain","domain":"waiting.context.test"}]}`).(map[string]any)
	if value["pending_count"] != float64(1) {
		t.Fatalf("unexpected discovery result: %v", value)
	}
	if results, ok := value["results"].([]any); ok && len(results) > 0 {
		t.Fatalf("pending IDs leaked: %v", results)
	}
	rows, err := d.Assets().ListTaskAssetApprovals(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, r := range rows {
		if r.Name == "waiting.context.test" {
			id = r.AssetID
		}
	}
	if id == 0 {
		t.Fatal("candidate missing from user approval list")
	}
	store := d.Exploration(task.ExplorationID)
	if waiting, err := store.WaitingForAssetApproval(); err != nil || !waiting {
		t.Fatalf("waiting=%v err=%v", waiting, err)
	}
	nodeID, err := store.AddNode("fact", map[string]any{"summary": "historic candidate"}, 0, "confirmed", "worker", []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	nodeStates, err := d.Assets().TaskNodeApprovalStates(task.ID, []int64{nodeID})
	if err != nil || nodeStates[nodeID] == db.ApprovalApproved {
		t.Fatalf("pending graph node allowed: %v %v", nodeStates, err)
	}
	p := assetContextProvider{assets: d.Assets(), taskID: task.ID}
	req := llm.CompletionRequest{Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "list1", Name: "list_assets", Input: json.RawMessage(`{}`)}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("list1", fmt.Sprintf(`{"assets":[{"id":%d,"type":"subdomain","domain":"waiting.context.test"}]}`, id), false)}},
	}}
	filtered, err := p.filter(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(filtered.Messages)
	if strings.Contains(string(encoded), "waiting.context.test") {
		t.Fatalf("pending history reached provider: %s", encoded)
	}
	if !strings.Contains(req.Messages[1].Content[0].Content[0].Text, "waiting.context.test") {
		t.Fatal("audit history mutated")
	}
	if err := d.Assets().ApproveTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	nodeStates, err = d.Assets().TaskNodeApprovalStates(task.ID, []int64{nodeID})
	if err != nil || nodeStates[nodeID] != db.ApprovalApproved {
		t.Fatalf("approved graph node hidden: %v %v", nodeStates, err)
	}
	filtered, err = p.filter(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(filtered.Messages)
	if !strings.Contains(string(encoded), "waiting.context.test") {
		t.Fatal("approved asset still hidden")
	}
	if waiting, err := store.WaitingForAssetApproval(); err != nil || waiting {
		t.Fatalf("still waiting after approval: %v %v", waiting, err)
	}
}
