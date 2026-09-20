package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/noa"
)

type compressTestHooks struct {
	calls int
	block bool
}

func (h *compressTestHooks) PreToolUse(_ context.Context, _ string, input []byte) (bool, string, []byte) {
	h.calls++
	return h.block, "inner hook", input
}
func (*compressTestHooks) PostToolUse(context.Context, string, []byte, []byte, bool) {}
func (*compressTestHooks) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return false, nil, ""
}

func TestCompressSkipsAssetInspectionButPreservesHooks(t *testing.T) {
	// An uninitialized store makes any accidental DB access fail this test.
	p := TaskAssetPolicy{Store: &db.AssetStore{}, TaskID: 1}
	input := []byte(`{"content":[{"startId":"m1","endId":"m2","summary":"https://api.hzbxhd.com)、hnlisu.com(45632"}],"asset_ids":[42]}`)
	for _, block := range []bool{false, true} {
		inner := &compressTestHooks{block: block}
		h := assetPolicyHooks{inner: inner, policy: p}
		blocked, reason, rewritten := h.PreToolUse(context.Background(), noa.CompressToolName, input)
		if blocked != block || reason != "inner hook" || string(rewritten) != string(input) || inner.calls != 1 {
			t.Fatalf("hook chain changed: %v %s %s", blocked, reason, rewritten)
		}
	}
}

func TestCompressAssetPolicyIsolation(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires explicit isolated ARTEX_PG_DSN")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("compress asset policy", "test", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	assets := d.Assets()
	host := fmt.Sprintf("compress-%d.test", task.ID)
	id, err := assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: host, TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	defer assets.DeleteByIDs([]int64{id})
	store := d.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "test"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClaimIntent(intent, "worker"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	input, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"startId": "m1", "endId": "m2", "summary": "https://api.hzbxhd.com)、hnlisu.com(45632 https://" + host}}, "asset_ids": []int64{id}})
	for _, state := range []string{db.ApprovalPending, db.ApprovalRevoked} {
		if state == db.ApprovalRevoked {
			if err := assets.RevokeTaskAssets(task.ID, []int64{id}, "test", "test"); err != nil {
				t.Fatal(err)
			}
		}
		for _, role := range []string{"mainagent", "planner", "worker"} {
			ctx := llmrec.WithRunInfo(context.Background(), llmrec.RunInfo{TaskID: task.ID, AgentKey: role, IntentID: intent})
			h := AssetPolicyHooks(nil, assets, task.ID)
			if blocked, reason, _ := h.PreToolUse(ctx, noa.CompressToolName, input); blocked {
				t.Fatal(state, role, reason)
			}
		}
		for _, table := range []string{"task_asset_skips", "task_worker_asset_access"} {
			var count int
			if err := d.QueryRow("SELECT count(*) FROM "+table+" WHERE task_id=$1", task.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("%s changed: %d %v", table, count, err)
			}
		}
		states, err := assets.TaskAssetApprovalStates(task.ID, []int64{id})
		if err != nil || states[id] != state {
			t.Fatal(states, err)
		}
	}
	// Similar names and real execution tools must retain authorization checks.
	input, _ = json.Marshal(map[string]string{"url": "https://" + host})
	for _, name := range []string{"Bash", "compress", "CompressCustom", "mcp__Compress"} {
		if reason := (TaskAssetPolicy{Store: assets, TaskID: task.ID}).Check(name, input); reason == "" {
			t.Fatalf("%s bypassed revoked asset", name)
		}
	}
}
