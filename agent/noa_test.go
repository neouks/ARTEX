package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

func TestNoaSwitchAndArchiveFallback(t *testing.T) {
	for _, mode := range []string{"unset", "off", "on", "unwritable"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if mode == "unwritable" {
				if err := os.WriteFile(filepath.Join(root, "noa"), []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &compaction.Config{}
			opts := agentcore.Options{Compaction: cfg}
			var enabled func() bool
			if mode != "unset" {
				enabled = func() bool { return mode != "off" }
			}
			warned := false
			enableNoa(&opts, enabled, root, "planner-1", func(string) { warned = true })
			if mode == "on" {
				if opts.Compactor == nil || opts.Compaction != nil || len(opts.Tools) != 1 || len(opts.AppendSystemPrompt) == 0 {
					t.Fatal("incomplete noa wiring")
				}
				if _, err := os.Stat(filepath.Join(root, "noa", "planner-1")); err != nil {
					t.Fatal(err)
				}
			} else {
				if opts.Compactor != nil || opts.Compaction != cfg || len(opts.Tools) != 0 || len(opts.AppendSystemPrompt) != 0 {
					t.Fatal("fallback changed existing compression")
				}
				if warned != (mode == "unwritable") {
					t.Fatalf("warning=%v", warned)
				}
			}
		})
	}
}

func TestNoaViewRefreshesTaskAssetAuthorization(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("noa approval", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := NewToolSet(d.Exploration(task.ExplorationID), "worker")
	ts.SetAssetStore(d.Assets(), d.Companies())
	ts.SetTaskID(task.ID)
	callReadJSON(t, ts.insertAssets(), `{"assets":[{"type":"subdomain","domain":"pending.noa-context.test"}]}`)
	rows, err := d.Assets().ListTaskAssetApprovals(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, r := range rows {
		if r.Name == "pending.noa-context.test" {
			id = r.AssetID
		}
	}
	if id == 0 {
		t.Fatal("missing discovery")
	}
	original := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "list", Name: "list_assets", Input: json.RawMessage(`{}`)}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("list", fmt.Sprintf(`{"assets":[{"id":%d,"domain":"pending.noa-context.test","type":"subdomain"}]}`, id), false)}},
	}
	opts := agentcore.Options{}
	enableNoa(&opts, func() bool { return true }, t.TempDir(), "planner", nil)
	view := opts.Compactor.(harness.ContextView).View(t.Context(), original)
	before, _ := json.Marshal(view)
	if !strings.Contains(string(before), "noa-ref") {
		t.Fatal("noa did not tag the fixture")
	}
	p := assetContextProvider{assets: d.Assets(), taskID: task.ID}
	ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, AgentKey: "planner"})
	for _, approved := range []bool{false, true} {
		if approved {
			if err := d.Assets().ApproveTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
				t.Fatal(err)
			}
		}
		out, err := p.filter(ctx, llm.CompletionRequest{Messages: view})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(out.Messages)
		if strings.Contains(string(body), "pending.noa-context.test") != approved {
			t.Fatalf("approval=%v view=%s", approved, body)
		}
		if !strings.Contains(string(body), "noa-ref") {
			t.Fatal("lost compression reference")
		}
	}
	after, _ := json.Marshal(view)
	if string(before) != string(after) {
		t.Fatal("mutated audit/noa view")
	}
}

func TestNoaUsesConfiguredModelWindow(t *testing.T) {
	opts := agentcore.Options{Compaction: &compaction.Config{ContextWindow: 40000}}
	enableNoa(&opts, func() bool { return true }, t.TempDir(), "small-window", nil)
	var history []llm.Message
	for i := 0; i < 20; i++ {
		history = append(history, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf("message %d: %s", i, strings.Repeat("history ", 625)))}})
	}
	view := opts.Compactor.(harness.ContextView).View(t.Context(), history)
	for _, m := range view {
		if strings.Contains(m.Text(), "HOW TO COMPRESS") {
			return
		}
	}
	t.Fatal("40k window did not request compression; noa may still be using its 200k fallback")
}
