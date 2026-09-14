package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestListAssetsApprovalDSL(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("approval DSL", "test", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	id, err := d.Assets().UpsertRootDomain(db.UpsertRootDomainReq{Domain: "approval-dsl-tool.invalid", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Assets().DeleteByIDs([]int64{id})
	ts := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	ts.SetAssetStore(d.Assets(), d.Companies())
	ts.SetTaskID(task.ID)
	for _, input := range []string{`{"dsl":"status==approved","limit":20}`, `{"dsl":"approval_state==approved","limit":20}`} {
		result, err := ts.listAssets().Call(t.Context(), json.RawMessage(input), nil)
		if err != nil || !strings.Contains(result.Flatten(), "approval-dsl-tool.invalid") {
			t.Fatalf("%s: %s %v", input, result.Flatten(), err)
		}
	}
}
