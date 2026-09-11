package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestSkipPromptScopesAndStableContent(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("scoped skips", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	node, err := d.Exploration(task.ExplorationID).AddNode("intent", map[string]any{"summary": "active work"}, 0, "open", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	workerScope := fmt.Sprintf("worker:%d", node)
	as := d.Assets()
	if _, err := as.RememberTaskAssetDenials(task.ID, []string{"own.test"}, nil, workerScope); err != nil {
		t.Fatal(err)
	}
	if _, err := as.RememberTaskAssetDenials(task.ID, []string{"other.test"}, nil, "worker:99999999"); err != nil {
		t.Fatal(err)
	}
	if _, err := as.RememberTaskAssetDenials(task.ID, []string{"plan.test"}, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	p := assetContextProvider{assets: as, taskID: task.ID}
	ctx := WithRunInfo(context.Background(), RunInfo{TaskID: task.ID, IntentID: node, AgentKey: "worker"})
	render := func(ctx context.Context) string {
		t.Helper()
		req, err := p.filter(ctx, llm.CompletionRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(req.System, "\n")
	}
	before := render(ctx)
	if !strings.Contains(before, "own.test") || strings.Contains(before, "other.test") || strings.Contains(before, "plan.test") {
		t.Fatalf("wrong worker scope: %s", before)
	}
	if strings.Count(before, db.TaskAssetSkipRule) != 1 {
		t.Fatal("fixed rule duplicated")
	}
	if _, err := as.RememberTaskAssetDenials(task.ID, []string{"own.test"}, nil, workerScope); err != nil {
		t.Fatal(err)
	}
	if after := render(ctx); after != before {
		t.Fatal("attempt count changed prompt")
	}
	plannerCtx := WithRunInfo(context.Background(), RunInfo{TaskID: task.ID, AgentKey: "planner"})
	planning := render(plannerCtx)
	if !strings.Contains(planning, "own.test") || !strings.Contains(planning, "plan.test") || strings.Contains(planning, "other.test") {
		t.Fatalf("wrong planning scope: %s", planning)
	}
	if err := d.Exploration(task.ExplorationID).SetNodeState(node, "done"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(render(plannerCtx), "own.test") {
		t.Fatal("completed worker polluted planner")
	}
	empty := render(WithRunInfo(context.Background(), RunInfo{TaskID: task.ID, IntentID: node + 1, AgentKey: "worker"}))
	if strings.Contains(empty, "跳过：") || strings.Contains(empty, "清单为空") {
		t.Fatal("empty list injected")
	}
	if _, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: "own.test", TaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(render(ctx), "own.test") {
		t.Fatal("approved host retained")
	}
}
