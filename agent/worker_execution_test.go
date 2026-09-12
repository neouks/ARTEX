package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/norma/llm"
)

type workerIndependentRestriction struct{}

func (workerIndependentRestriction) PreToolUse(context.Context, string, []byte) (bool, string, []byte) {
	return true, "independent restriction", nil
}
func (workerIndependentRestriction) PostToolUse(context.Context, string, []byte, []byte, bool) {}
func (workerIndependentRestriction) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return false, nil, ""
}

func TestWorkerPendingExecutionKeepsPlannerBoundary(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("worker execution", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as, es := d.Assets(), d.Exploration(task.ExplorationID)
	a, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: "start.worker.test", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := es.AddNode(db.KindIntent, map[string]any{"summary": "test A"}, 1, "open", "planner", []int64{a})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := es.ClaimIntent(intent, "worker"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	worker := NewToolSet(es, "worker")
	worker.SetAssetStore(as, d.Companies())
	worker.SetTaskID(task.ID)
	worker.SetOwnerNode(intent)
	worker.workerExecution = true
	ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, IntentID: intent, AgentKey: "worker"})
	hooks := guard.AssetPolicyHooks(nil, as, task.ID)
	command := []byte(`{"command":"curl https://new.worker.test/x"}`)
	if blocked, why, _ := hooks.PreToolUse(ctx, "Bash", command); blocked {
		t.Fatal(why)
	}
	independent := guard.AssetPolicyHooks(workerIndependentRestriction{}, as, task.ID)
	if blocked, why, _ := independent.PreToolUse(ctx, "Bash", command); !blocked || why != "independent restriction" {
		t.Fatal("independent guard bypassed")
	}
	if blocked, _, _ := hooks.PreToolUse(WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, AgentKey: "planner"}), "Bash", command); !blocked {
		t.Fatal("planner allowed pending")
	}
	value := callReadJSON(t, worker.insertAssets(), `{"assets":[{"type":"root_domain","domain":"new.worker.test"}]}`).(map[string]any)
	results := value["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("pending discovery hidden: %v", value)
	}
	row := results[0].(map[string]any)
	b := int64(row["id"].(float64))
	if row["approval_state"] != "pending" {
		t.Fatalf("auto approved: %v", row)
	}
	if err := as.ValidateTaskAssetsApproved(task.ID, []int64{b}); err == nil {
		t.Fatal("pending became schedulable")
	}
	foreign, err := d.CreateTaskWithOptions("foreign execution", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(foreign.ID)
	foreignID, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: "foreign.worker.test", TaskID: foreign.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.validateResultAssets([]int64{foreignID}); err == nil {
		t.Fatal("cross-task asset allowed")
	}
	for _, badCtx := range []RunInfo{{TaskID: foreign.ID, IntentID: intent, AgentKey: "worker"}, {TaskID: task.ID, IntentID: intent, AgentKey: "mainagent"}} {
		if blocked, _, _ := hooks.PreToolUse(WithRunInfo(t.Context(), badCtx), "Bash", command); !blocked {
			t.Fatal("forged/mismatched role allowed")
		}
	}
	if blocked, _, _ := hooks.PreToolUse(ctx, "Bash", []byte(`{"command":"curl https://www_host/x"}`)); !blocked {
		t.Fatal("invalid host allowed")
	}
	inherited, err := d.CreateTaskWithOptions("inherited worker", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets", SourceTaskIDs: []int64{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(inherited.ID)
	rows, err := as.WithWorkerRead().ToolAssetsByIDs(inherited.ID, []int64{b})
	if err != nil || len(rows) != 1 || !rows[0].TaskReadOnly || rows[0].ApprovalState != db.ApprovalPending {
		t.Fatalf("source projection: %+v %v", rows, err)
	}
	service, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://new.worker.test:8443/x", TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := as.ValidateWorkerAssets(task.ID, []int64{service}); err != nil {
		t.Fatal("pending service restricted", err)
	}
	anchors, err := es.LineageAnchorAssetIDs(intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range anchors {
		if id == b {
			t.Fatal("execution became planning anchor")
		}
	}
	read := callReadJSON(t, worker.listAssets(), fmt.Sprintf(`{"id":%d}`, b)).(map[string]any)
	if len(read["assets"].([]any)) != 1 {
		t.Fatalf("pending not readable: %v", read)
	}
	fact, err := worker.recordOneFact(factItem{Summary: "observed B", AssetIDs: []json.RawMessage{json.RawMessage(fmt.Sprint(b))}}, intent)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.addFinding().Call(ctx, json.RawMessage(fmt.Sprintf(`{"summary":"B finding","vulnclass":"test","severity":"high","intent_id":%d,"asset_ids":[%d]}`, intent, b)), nil)
	if err != nil || result.IsError {
		t.Fatalf("finding: %v %s", err, result.Flatten())
	}
	var tested bool
	if err := d.QueryRow(`SELECT tested FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, b).Scan(&tested); err != nil || !tested {
		t.Fatalf("tested: %v %v", tested, err)
	}
	planner := NewToolSet(es, "planner")
	planner.SetAssetStore(as, d.Companies())
	planner.SetTaskID(task.ID)
	if _, err := planner.addOneIntent(intentItem{Summary: "test B", AssetIDs: []json.RawMessage{json.RawMessage(fmt.Sprint(b))}}); err == nil {
		t.Fatal("planner scheduled pending ID")
	}
	if _, err := planner.addOneIntent(intentItem{Summary: "follow fact", ParentIDs: []json.RawMessage{json.RawMessage(fmt.Sprint(fact))}}); err == nil {
		t.Fatal("planner scheduled pending lineage")
	}
	pendingIntent, err := es.AddNode(db.KindIntent, map[string]any{"summary": "old pending"}, 1, "open", "planner", []int64{b})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := es.ClaimIntent(pendingIntent, "worker"); err != nil || ok {
		t.Fatalf("pending claimed %v %v", ok, err)
	}
	provider := assetContextProvider{assets: as, taskID: task.ID}
	req := llm.CompletionRequest{Messages: []llm.Message{{Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "a", Name: "list_assets"}, llm.ToolResultText("a", fmt.Sprintf(`{"assets":[{"id":%d,"type":"root_domain","domain":"new.worker.test","approval_state":"pending"}]}`, b), false)}}}}
	filtered, err := provider.filter(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(filtered.Messages)
	if !strings.Contains(string(encoded), "new.worker.test") || strings.Contains(strings.Join(filtered.System, "\n"), "new.worker.test（等待审批）") {
		t.Fatal("pending still hidden/skipped")
	}
	for _, operation := range []string{"block", "revoke", "delete"} {
		switch operation {
		case "block":
			err = as.BlockTaskAssets(task.ID, []int64{b}, "user", "")
		case "revoke":
			err = as.RevokeTaskAssets(task.ID, []int64{b}, "user", "")
		case "delete":
			_, err = as.DetachAssetFromTask(task.ID, b)
		}
		if err != nil {
			t.Fatal(err)
		}
		if blocked, _, _ := hooks.PreToolUse(ctx, "Bash", command); !blocked {
			t.Fatalf("%s bypass", operation)
		}
		if err := worker.validateResultAssets([]int64{b}); err == nil {
			t.Fatalf("%s write allowed", operation)
		}
		if err := as.ValidateWorkerAssets(task.ID, []int64{service}); err == nil {
			t.Fatalf("%s parent service bypass", operation)
		}
		ids, err := as.RunningIntentIDsForAssets(task.ID, []int64{b})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, id := range ids {
			if id == intent {
				found = true
			}
		}
		if !found {
			t.Fatal("execution not selected for cancellation")
		}
		if operation != "delete" {
			if err := as.ApproveTaskAssets(task.ID, []int64{b}, "user", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
}
