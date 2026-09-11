package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestToolAssetsSummaryAndExplicitCredentials(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("tool detail", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	secret := strings.Repeat("secret-token-中文", 4000)
	id, err := d.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://tool-budget.test/", TaskID: task.ID, Auth: []map[string]any{{"token": secret}}, PageTitle: "test title"})
	if err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	tools.SetAssetStore(d.Assets(), d.Companies())
	tools.SetTaskID(task.ID)
	result, err := tools.listAssets().Call(t.Context(), json.RawMessage(fmt.Sprintf(`{"id":%d}`, id)), nil)
	if err != nil || result.IsError {
		t.Fatalf("summary: %s %v", result.Flatten(), err)
	}
	if strings.Contains(result.Flatten(), "secret-token") {
		t.Fatal("credential in default summary")
	}
	raw, err := d.Assets().GetByIDs([]int64{id})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(raw)
	t.Logf("asset raw=%d bytes model summary=%d bytes", len(encoded), len(result.Flatten()))
	for _, detail := range []bool{false, true} {
		assets, err := d.Assets().WithToolReadFields(detail, []string{"identity"}).ToolAssetsByIDs(task.ID, []int64{id})
		if err != nil || len(assets) != 1 || len(assets[0].Auth) > 0 {
			t.Fatalf("unselected auth was fetched: %v", err)
		}
	}
	var restored strings.Builder
	for offset := 0; ; {
		query := fmt.Sprintf(`{"id":%d,"detail":true,"fields":["auth"],"field":"/auth/0/token","text_offset":%d}`, id, offset)
		result, err := tools.listAssets().Call(t.Context(), json.RawMessage(query), nil)
		if err != nil || result.IsError {
			t.Fatalf("detail: %s %v", result.Flatten(), err)
		}
		if len([]rune(result.Flatten())) > toolListBudget {
			t.Fatal("response exceeded budget")
		}
		var page struct {
			Assets []struct {
				Details struct {
					Value      string `json:"value"`
					NextOffset int    `json:"next_offset"`
					Truncated  bool   `json:"truncated"`
				} `json:"details"`
			} `json:"assets"`
		}
		if err := json.Unmarshal([]byte(result.Flatten()), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Assets) != 1 {
			t.Fatal("asset missing")
		}
		body := page.Assets[0].Details
		restored.WriteString(body.Value)
		if !body.Truncated {
			break
		}
		if body.NextOffset <= offset {
			t.Fatal("cursor did not advance")
		}
		offset = body.NextOffset
	}
	if restored.String() != secret {
		t.Fatal("credential detail not reconstructible")
	}
	for _, query := range []string{fmt.Sprintf(`{"id":%d,"dsl":"test"}`, id), `{"dsl":"test","limit":51}`, `{"dsl":"test","detail":true}`, `{"dsl":"test","fields":["auth"]}`} {
		r, _ := tools.listAssets().Call(t.Context(), json.RawMessage(query), nil)
		if !r.IsError {
			t.Fatalf("accepted invalid %s", query)
		}
	}
}

func TestOverviewReadScopeDoesNotOutliveCall(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("overview scope", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	asset, err := d.Assets().UpsertRootDomain(db.UpsertRootDomainReq{Domain: fmt.Sprintf("scope-%d.test", task.ID), TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(task.ExplorationID)
	if _, err := store.AddNode(db.KindFact, map[string]any{"summary": "scope cache"}, 0, "confirmed", "test", []int64{asset}); err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(store, "planner")
	tools.SetAssetStore(d.Assets(), d.Companies())
	tools.SetTaskID(task.ID)
	before := tools.graphOverviewData()
	if tools.overviewReads != nil {
		t.Fatal("request cache escaped into shared ToolSet")
	}
	if err := d.Assets().RevokeTaskAssets(task.ID, []int64{asset}, "user", ""); err != nil {
		t.Fatal(err)
	}
	after := tools.graphOverviewData()
	if before["facts"].(int) <= after["facts"].(int) {
		t.Fatalf("authorization was cached across calls: %v -> %v", before["facts"], after["facts"])
	}
	if tools.overviewReads != nil {
		t.Fatal("shared scope was mutated")
	}
}
