package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGraphOverviewWithoutTaskReturnsToolError(t *testing.T) {
	for _, ts := range []*ToolSet{nil, NewToolSet(nil, "planner")} {
		result, err := ts.GraphOverviewTool().Call(t.Context(), json.RawMessage(`{}`), nil)
		if err != nil || !result.IsError || !strings.Contains(result.Flatten(), "任务探索上下文") {
			t.Fatalf("missing task result=%+v err=%v", result, err)
		}
		if ts.graphOverviewData()["error"] == nil {
			t.Fatal("prefetch must not report empty success")
		}
	}
}
