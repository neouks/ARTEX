package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestFindingDeletionFeedbackBudget(t *testing.T) {
	rows := []db.FindingDeletionFeedback{}
	for i := int64(50); i > 0; i-- {
		rows = append(rows, db.FindingDeletionFeedback{ID: i, FindingID: i, Title: strings.Repeat("标题", 100), Reason: strings.Repeat("证据不足", 400), VulnClass: "XSS"})
	}
	page := packDeletionFeedback(rows, true)
	raw, _ := json.Marshal(page)
	if len(page.Groups) != 1 || len(page.Groups[0].Findings) < 2 || !page.HasMore || page.NextBefore <= 0 || len([]rune(string(raw))) > 6000 {
		t.Fatalf("bad budget %d %+v", len([]rune(string(raw))), page)
	}
	escaped := packDeletionFeedback([]db.FindingDeletionFeedback{{ID: 1, Reason: strings.Repeat("<", 2000)}}, false)
	encoded, _ := json.Marshal(escaped)
	if len(escaped.Groups) != 1 || len(escaped.Groups[0].TruncatedFields) == 0 || len([]rune(string(encoded))) > 6000 || escaped.NextBefore != 1 {
		t.Fatal("oversized row cannot be continued")
	}
	empty := packDeletionFeedback(nil, false)
	if empty.HasMore || len(empty.Groups) != 0 {
		t.Fatal("bad empty page")
	}
}
func TestFindingDeletionFeedbackRoleAndPagination(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("feedback page", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	ts.SetTaskID(task.ID)
	for i := 0; i < 3; i++ {
		id, e := d.AddFinding(task.ID, 0, "XSS", "title", "high", "", "", "worker", nil)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = d.DeleteFindingWithFeedback(id, "same reason"); e != nil {
			t.Fatal(e)
		}
	}
	tool := ts.listFindingDeletionFeedback()
	for _, ri := range []RunInfo{{TaskID: task.ID, AgentKey: "worker"}, {TaskID: task.ID, AgentKey: "mainagent"}, {TaskID: task.ID + 1, AgentKey: "planner"}} {
		r, _ := tool.Call(WithRunInfo(t.Context(), ri), json.RawMessage(`{}`), nil)
		if !r.IsError {
			t.Fatal("role/task bypass")
		}
	}
	p, err := ts.findingDeletionFeedbackPage(0, 2)
	if err != nil || !p.HasMore || len(p.Groups) != 1 || len(p.Groups[0].Findings) != 2 {
		t.Fatalf("page %+v %v", p, err)
	}
	next, err := ts.findingDeletionFeedbackPage(p.NextBefore, 2)
	if err != nil || next.HasMore || len(next.Groups[0].Findings) != 1 {
		t.Fatalf("next %+v %v", next, err)
	}
	v, err := ts.ts.FindingDeletionFeedbackField(p.Groups[0].Findings[0].ID, "reason")
	if err != nil || v != "same reason" {
		t.Fatalf("field %q %v", v, err)
	}
	for _, raw := range []string{`{"limit":51}`, `{"before":-1}`, `{"task_id":1}`} {
		r, _ := tool.Call(WithRunInfo(t.Context(), RunInfo{TaskID: task.ID, AgentKey: "planner"}), json.RawMessage(raw), nil)
		if !r.IsError {
			t.Fatal("bad input", raw)
		}
	}
}
