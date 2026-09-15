package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

func TestTargetAccessInputAndHistory(t *testing.T) {
	for _, raw := range []string{`{}`, `{"asset_ids":[0]}`, `{"node_ids":[-1]}`, `{"node_ids":[1.5]}`, `{"task_id":1}`, `null`} {
		if _, err := decodeTargetAccess(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	raw, _ := json.Marshal(targetAccessQuery{AssetIDs: make([]int64, 51)})
	if _, err := decodeTargetAccess(raw); err == nil {
		t.Fatal("accepted oversized batch")
	}
	a := normalizedReadQuery("check_target_access", json.RawMessage(`{"asset_ids":[2,1,2],"node_ids":[3]}`))
	b := normalizedReadQuery("check_target_access", json.RawMessage(`{"node_ids":[3],"asset_ids":[1,2]}`))
	if a == "" || a != b {
		t.Fatalf("dedup keys %s %s", a, b)
	}
	for _, tool := range NewToolSet(nil, "worker").WorkerTools() {
		if tool.Name() == "check_target_access" {
			t.Fatal("worker received preflight")
		}
	}
}

func TestTargetAccessApprovalLifecycle(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("target access", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as := d.Assets()
	ts := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	ts.SetAssetStore(as, d.Companies())
	ts.SetTaskID(task.ID)
	ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, AgentKey: "planner"})
	makeAsset := func(host string) int64 {
		id, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: host, TaskID: task.ID, AgentDiscovered: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	pending := makeAsset("pending.access.test")
	approved := makeAsset("approved.access.test")
	if err := as.ApproveTaskAssets(task.ID, []int64{approved}, "user", ""); err != nil {
		t.Fatal(err)
	}
	node, err := ts.ts.AddNode(db.KindFact, map[string]any{"summary": "sensitive-evidence", "body": strings.Repeat("private-body", 1000)}, 0, "confirmed", "test", []int64{pending})
	if err != nil {
		t.Fatal(err)
	}
	query := fmt.Sprintf(`{"asset_ids":[%d,%d,99999999],"node_ids":[%d]}`, pending, approved, node)
	tool := ts.checkTargetAccess()
	call := func(raw string) targetAccessView {
		r, e := tool.Call(ctx, json.RawMessage(raw), nil)
		if e != nil || r.IsError {
			t.Fatalf("query %s %v", r.Flatten(), e)
		}
		var v targetAccessView
		if e = json.Unmarshal([]byte(r.Flatten()), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	v := call(query)
	if v.Assets[0].State != "pending" || v.Assets[1].State != "approved" || v.Assets[2].State != "unavailable" || v.Nodes[0].CanRead || !reflect.DeepEqual(v.Nodes[0].Reasons, []string{"pending"}) {
		t.Fatalf("states %+v", v)
	}
	for _, ri := range []RunInfo{{TaskID: task.ID, AgentKey: "worker"}, {TaskID: task.ID + 1, AgentKey: "planner"}, {AgentKey: "mainagent"}} {
		r, _ := tool.Call(WithRunInfo(t.Context(), ri), json.RawMessage(query), nil)
		if !r.IsError {
			t.Fatal("context bypass")
		}
	}
	if reason := (guard.TaskAssetPolicy{Store: as, TaskID: task.ID}).Check("check_target_access", []byte(query)); reason != "" {
		t.Fatal(reason)
	}
	r, _ := ts.nodeDetail().Call(ctx, json.RawMessage(fmt.Sprintf(`{"id":%d}`, node)), nil)
	if !r.IsError || strings.Contains(r.Flatten(), "sensitive-evidence") || !strings.Contains(r.Flatten(), "pending") {
		t.Fatalf("detail %s", r.Flatten())
	}
	deniedDetail := r.Flatten()
	batch := fmt.Sprintf(`{"intents":[{"summary":"skip pending","asset_ids":[%d]},{"summary":"check approved","asset_ids":[%d]},{"summary":"check https://pending.access.test/"}]}`, pending, approved)
	if reason := (guard.TaskAssetPolicy{Store: as, TaskID: task.ID}).Check("add_intent", []byte(batch)); reason != "" {
		t.Fatal("whole batch blocked", reason)
	}
	r, e := ts.addIntent().Call(ctx, json.RawMessage(batch), nil)
	if e != nil {
		t.Fatal(e)
	}
	var result struct {
		IDs    []int64                     `json:"ids"`
		Errors map[string]string           `json:"errors"`
		Access map[string]targetAccessView `json:"access_errors"`
	}
	if err := json.Unmarshal([]byte(r.Flatten()), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.IDs) != 3 || result.IDs[0] != 0 || result.IDs[1] <= 0 || result.IDs[2] != 0 || len(result.Errors) != 2 || len(result.Access) != 2 {
		t.Fatalf("batch %s", r.Flatten())
	}
	nodes, err := ts.ts.ListByKind(db.KindIntent, 100)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("residual intents %v %v", nodes, err)
	}
	input := json.RawMessage(fmt.Sprintf(`{"node_ids":[%d],"asset_ids":[%d]}`, node, pending))
	body, _ := json.Marshal(map[string]any{"id": node, "kind": "fact", "state": "confirmed", "payload": map[string]any{"body": strings.Repeat("private-body", 1000)}})
	preflight, _ := tool.Call(ctx, input, nil)
	req := llm.CompletionRequest{Messages: []llm.Message{{Content: []llm.ContentBlock{
		{Type: llm.BlockToolUse, ID: "detail", Name: "node_detail", Input: json.RawMessage(fmt.Sprintf(`{"id":%d}`, node))}, llm.ToolResultText("detail", string(body), false),
		{Type: llm.BlockToolUse, ID: "access", Name: "check_target_access", Input: input}, llm.ToolResultText("access", preflight.Flatten(), false),
	}}}}
	req.Messages[0].Content = append(req.Messages[0].Content,
		llm.ContentBlock{Type: llm.BlockToolUse, ID: "old-denial", Name: "node_detail", Input: json.RawMessage(fmt.Sprintf(`{"id":%d}`, node))},
		llm.ToolResultText("old-denial", deniedDetail, true))
	original, _ := json.Marshal(req)
	p := assetContextProvider{assets: as, taskID: task.ID}
	filtered, err := p.filter(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(filtered)
	if strings.Contains(string(encoded), "private-body") || !strings.Contains(strings.Join(filtered.System, ""), "pending") {
		t.Fatal("context not compacted")
	}
	t.Logf("model input bytes before=%d after=%d", len(original), len(encoded))
	if len(encoded) >= len(original) {
		t.Fatal("restricted context did not shrink")
	}
	if err := as.ApproveTaskAssets(task.ID, []int64{pending}, "user", ""); err != nil {
		t.Fatal(err)
	}
	filtered, err = p.filter(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(filtered.System, ""), "当前候选审批：") {
		t.Fatal("stale restriction after approval")
	}
	if !strings.Contains(filtered.Messages[0].Content[3].Content[0].Text, `"can_read":true`) {
		t.Fatal("preflight history not refreshed")
	}
	if text := filtered.Messages[0].Content[5].Content[0].Text; !strings.Contains(text, `"can_read":true`) || !strings.Contains(text, `"historical_error":true`) {
		t.Fatalf("stale denial: %s", text)
	}
	if err := as.RevokeTaskAssets(task.ID, []int64{pending}, "user", ""); err != nil {
		t.Fatal(err)
	}
	v = call(query)
	if v.Nodes[0].CanRead || !reflect.DeepEqual(v.Nodes[0].Reasons, []string{"revoked"}) {
		t.Fatalf("revoke %+v", v)
	}
	if _, err := ts.addOneIntent(intentItem{Summary: "after revoke", ParentIDs: []json.RawMessage{json.RawMessage(fmt.Sprint(node))}}); err == nil {
		t.Fatal("stale authorization admitted")
	}
	if err := as.BlockTaskAssets(task.ID, []int64{pending}, "user", ""); err != nil {
		t.Fatal(err)
	}
	v = call(query)
	if !reflect.DeepEqual(v.Nodes[0].Reasons, []string{"blocked"}) {
		t.Fatalf("block %+v", v)
	}
	after, _ := json.Marshal(req)
	if string(after) != string(original) {
		t.Fatal("audit mutated")
	}
}

func TestTargetAccessUnavailableAndCap(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("access cap", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, AgentKey: "planner"})
	req := llm.CompletionRequest{}
	for i := 1; i <= 60; i++ {
		req.Messages = append(req.Messages, llm.Message{Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: fmt.Sprint(i), Name: "node_detail", Input: json.RawMessage(fmt.Sprintf(`{"id":%d}`, 90000000+i))}}})
	}
	p := assetContextProvider{assets: d.Assets(), taskID: task.ID}
	out, err := p.filter(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Access    targetAccessView `json:"target_access"`
		Truncated bool             `json:"truncated"`
	}
	for _, s := range out.System {
		if strings.HasPrefix(s, "当前候选审批：") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(s, "当前候选审批：")), &summary); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !summary.Truncated || len(summary.Access.Nodes) != 50 || summary.Access.Nodes[0].ID != 90000060 || summary.Access.Nodes[49].ID != 90000011 {
		t.Fatalf("cap %+v", summary)
	}
	again, err := p.filter(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range again.System {
		if strings.HasPrefix(s, "当前候选审批：") {
			n++
		}
	}
	if n != 1 {
		t.Fatal("status accumulated")
	}
	tool := NewToolSet(nil, "planner").checkTargetAccess()
	if err := actool.ValidateInput(tool.InputSchema(), json.RawMessage(`{"asset_ids":[-1]}`)); err == nil {
		t.Fatal("schema accepted invalid ID")
	}
}
