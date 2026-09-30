package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

func TestStructuredCaptureIsBoundedAndRevalidated(t *testing.T) {
	pg := testDB(t)
	defer pg.Close()
	task, err := pg.CreateTaskWithOptions("captured authorization", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "all_assets"})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteTask(task.ID)
	as, store := pg.Assets(), pg.Exploration(task.ExplorationID)
	asset, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: fmt.Sprintf("capture-%d.fixture.test", task.ID), TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.AddNode(db.KindFact, map[string]any{"summary": "protected-evidence"}, 0, "confirmed", "worker", []int64{asset})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("protected-evidence 中文🙂\n", 4000)
	base, err := jsonResult(map[string]any{"id": node, "kind": "fact", "state": "confirmed", "summary": long, "asset_ids": []int64{asset}})
	if err != nil {
		t.Fatal(err)
	}
	originalBody := base.Flatten()
	requests := []llm.CompletionRequest{}
	for _, write := range []bool{false, true} {
		dir := t.TempDir()
		tc := &actool.ToolContext{OutputDir: dir, MaxOutputChars: 2048}
		run := func(context.Context, json.RawMessage) (actool.Result, error) { return base, nil }
		var target actool.CoreTool
		if write {
			target = writeTool("insert_assets", "", obj(nil), run)
		} else {
			target = readTool("node_detail", "", obj(nil), run)
		}
		res, err := target.Call(t.Context(), json.RawMessage(`{}`), tc)
		if err != nil || !json.Valid([]byte(res.Flatten())) || len(res.Flatten()) > 2048 || !utf8.ValidString(res.Flatten()) {
			t.Fatalf("invalid bounded JSON: bytes=%d err=%v", len(res.Flatten()), err)
		}
		if !strings.Contains(res.Flatten(), "protected-evidence") || !strings.Contains(res.Flatten(), "persisted-output") {
			t.Fatal("lost useful authorized preview or source pointer")
		}
		if actool.CaptureOnce(tc, res.Flatten()) != res.Flatten() {
			t.Fatal("global cap modified valid bounded envelope")
		}
		files, err := filepath.Glob(filepath.Join(dir, "output-*.txt"))
		if err != nil || len(files) != 1 {
			t.Fatal("duplicate capture", files, err)
		}
		raw, err := os.ReadFile(files[0])
		if err != nil || string(raw) != base.Flatten() {
			t.Fatal("full original result lost", err)
		}
		for _, deferred := range []bool{false, true} {
			call := llm.ContentBlock{Type: llm.BlockToolUse, ID: "call", Name: target.Name(), Input: json.RawMessage(fmt.Sprintf(`{"id":%d}`, node))}
			if deferred {
				call.Name = actool.ExecuteExtraToolName
				call.Input, _ = json.Marshal(map[string]any{"tool_name": target.Name(), "params": map[string]any{"id": node}})
			}
			requests = append(requests, llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentBlock{call}}, {Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("call", res.Flatten(), false)}}}})
		}
	}
	if base.Flatten() != originalBody {
		t.Fatal("source result changed")
	}
	for _, role := range []string{"planner", "mainagent", "worker"} {
		ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, ExplorationID: store.ID(), AgentKey: role})
		if role == "worker" {
			ctx = WithRunInfo(ctx, RunInfo{IntentID: 999})
		}
		for _, req := range requests {
			filtered, err := FilterTaskAssetRequest(ctx, as, task.ID, req)
			if err != nil || !strings.Contains(filtered.Messages[1].Content[0].Content[0].Text, "protected-evidence") {
				t.Fatalf("authorized %s preview hidden: err=%v", role, err)
			}
		}
	}
	if err := as.RevokeTaskAssets(task.ID, []int64{asset}, "user", ""); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"planner", "mainagent", "worker"} {
		ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, ExplorationID: store.ID(), AgentKey: role})
		if role == "worker" {
			ctx = WithRunInfo(ctx, RunInfo{IntentID: 999})
		}
		for _, req := range requests {
			before, _ := json.Marshal(req)
			filtered, err := FilterTaskAssetRequest(ctx, as, task.ID, req)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(filtered.Messages)
			if strings.Contains(string(encoded), "protected-evidence") || strings.Contains(string(encoded), "persisted-output") {
				t.Fatalf("revoked %s preview or pointer leaked", role)
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("audit source mutated")
			}
		}
	}
}

func TestStructuredCaptureKeepsAllIDsOrDropsPreview(t *testing.T) {
	for _, max := range []int{10, 80, 1000, 30000} {
		original, _ := jsonResult(map[string]any{"asset_ids": []int64{1, 2}, "summary": strings.Repeat("secret 中文\n", 10000), "id": 3})
		out := captureStructuredResult("node_detail", original, &actool.ToolContext{MaxOutputChars: max})
		if !json.Valid([]byte(out.Flatten())) || len(out.Flatten()) > max {
			t.Fatalf("bad budget max=%d bytes=%d", max, len(out.Flatten()))
		}
		var value any
		json.Unmarshal([]byte(out.Flatten()), &value)
		assets, nodes := map[int64]bool{}, map[int64]bool{}
		filterStructuredAssets(value, nil, assets)
		filterStructuredRows(value, nil, nodes, structuredNodeIDs)
		if strings.Contains(out.Flatten(), "secret") && (!assets[1] || !assets[2] || !nodes[3]) {
			t.Fatal("preview retained without all authorization ids")
		}
	}
	short := actool.Text(`{"assets":[]}`)
	if out := captureStructuredResult("list_assets", short, &actool.ToolContext{}); out.Flatten() != short.Flatten() {
		t.Fatal("small success protocol changed")
	}
}
