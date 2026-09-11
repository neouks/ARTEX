package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestFindingsPaginationRules(t *testing.T) {
	description := NewToolSet(nil, "planner").listFindings().Description()
	for _, rule := range []string{"q 仅搜索摘要", "has_more=true", "清除 before", "尚未查完", "当前授权可见记录中"} {
		if !strings.Contains(description, rule) {
			t.Fatalf("missing rule %q", rule)
		}
	}
	if strings.Contains(plannerDefaultTmpl, "全部漏洞") || !strings.Contains(plannerDefaultTmpl, "未查完不能断言不存在") {
		t.Fatal("planner pagination contract is stale")
	}
}

func TestFindingsPaginationContinuationAndSearch(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("finding pagination", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as := d.Assets()
	approved, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: "finding-page.test", TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: "finding-hidden.test", TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	es := d.Exploration(task.ExplorationID)
	add := func(summary string, asset int64) int64 {
		t.Helper()
		id, err := es.AddNode(db.KindFinding, map[string]any{"summary": summary, "severity": "high", "evidence": "正文专属关键词"}, 0, "confirmed", "test", []int64{asset})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	target := add("分页目标", approved)
	add("分页其他一", approved)
	add("分页其他二", approved)
	add("分页目标隐藏", pending)
	ts := NewToolSet(es, "planner")
	ts.SetAssetStore(as, d.Companies())
	ts.SetTaskID(task.ID)
	type page struct {
		Findings []struct {
			ID int64 `json:"id"`
		} `json:"findings"`
		Total     int    `json:"total"`
		More      bool   `json:"has_more"`
		Next      int64  `json:"next_before"`
		Hint      string `json:"read_hint"`
		Truncated bool   `json:"truncated"`
	}
	read := func(q string) page {
		t.Helper()
		r, err := ts.listFindings().Call(t.Context(), json.RawMessage(q), nil)
		if err != nil || r.IsError {
			t.Fatalf("query failed: %v %s", err, r.Flatten())
		}
		var p page
		if err := json.Unmarshal([]byte(r.Flatten()), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := read(fmt.Sprintf(`{"asset_id":%d,"q":"分页","severity":"high","limit":2}`, approved))
	if first.Total != 3 || len(first.Findings) != 2 || !first.More || first.Hint == "" || first.Next == 0 {
		t.Fatalf("first: %+v", first)
	}
	second := read(fmt.Sprintf(`{"asset_id":%d,"q":"分页","severity":"high","limit":2,"before":%d}`, approved, first.Next))
	if len(second.Findings) != 1 || second.Findings[0].ID != target || second.More || second.Hint != "" {
		t.Fatalf("second: %+v", second)
	}
	search := read(`{"q":"分页目标","severity":"high","limit":2}`)
	if search.Total != 1 || len(search.Findings) != 1 || search.Findings[0].ID != target {
		t.Fatalf("search: %+v", search)
	}
	if p := read(`{"q":"正文专属关键词"}`); p.Total != 0 {
		t.Fatal("q unexpectedly searches evidence")
	}
	for i := 0; i < 100; i++ {
		add("预算"+strings.Repeat("长", 355), approved)
	}
	before, count := int64(0), 0
	for {
		p := read(fmt.Sprintf(`{"q":"预算","limit":100,"before":%d}`, before))
		if before == 0 && !p.Truncated {
			t.Fatal("fixture did not trigger response budget")
		}
		count += len(p.Findings)
		if !p.More {
			break
		}
		if len(p.Findings) == 0 || p.Hint == "" || p.Next != p.Findings[len(p.Findings)-1].ID || (before > 0 && p.Next >= before) {
			t.Fatalf("invalid continuation: %+v", p)
		}
		before = p.Next
	}
	if count != 100 {
		t.Fatalf("lost findings: %d", count)
	}
}
