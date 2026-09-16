package agent

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func TestInsertAssetsInvocationOrigins(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("origins", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	es := d.Exploration(task.ExplorationID)
	ts := NewToolSet(es, "human")
	ts.SetTaskID(task.ID)
	ts.SetAssetStore(d.Assets(), d.Companies())
	ctx := WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, ExplorationID: task.ExplorationID, AgentKey: "mainagent"})
	var wg sync.WaitGroup
	for i := range 2 {
		call := fmt.Sprintf("invocation-%d", i)
		seg := i
		aid, e := es.AppendActivity(db.Activity{Worker: "mainagent", MainSeg: &seg, Kind: "tool_use", Tool: "insert_assets", ToolUseID: call})
		if e != nil {
			t.Fatal(e)
		}
		wg.Go(func() {
			host := fmt.Sprintf("origin-%d-%d.test", task.ID, i)
			raw, _ := json.Marshal(map[string]any{"assets": []any{map[string]any{"type": "subdomain", "domain": host}, map[string]any{"type": "ip", "ip": "not-an-ip"}}})
			result, e := ts.insertAssets().Call(ctx, raw, &actool.ToolContext{ToolUseID: call})
			if e != nil {
				t.Error(e)
				return
			}
			var out struct {
				Results []struct {
					ID int64 `json:"id"`
				} `json:"results"`
				Errors []any `json:"errors"`
			}
			if e = json.Unmarshal([]byte(result.Flatten()), &out); e != nil {
				t.Error(e)
				return
			}
			if len(out.Results) != 1 || len(out.Errors) != 1 {
				t.Errorf("batch %s", result.Flatten())
				return
			}
			var origin db.AssetOrigin
			var b []byte
			if e = d.QueryRow(`SELECT source_origin FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, task.ID, out.Results[0].ID).Scan(&b); e != nil {
				t.Error(e)
				return
			}
			if e = json.Unmarshal(b, &origin); e != nil {
				t.Error(e)
				return
			}
			if origin.ActivityID != aid || origin.ToolUseID != call || origin.Session != fmt.Sprintf("main:%d", i) {
				t.Errorf("wrong source %+v", origin)
			}
		})
	}
	wg.Wait()
}
