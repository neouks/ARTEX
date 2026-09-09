package db

import (
	"testing"
	"time"
)

func TestMCPUsageAggregatesAndRecentCalls(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()
	const serverID int64 = 922337
	_, _ = d.Exec(`DELETE FROM mcp_usage WHERE server_id=$1`, serverID)
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM mcp_usage WHERE server_id=$1`, serverID) })
	for _, usage := range []*MCPUsage{
		{ServerID: serverID, ServerName: "usage-test", ToolName: "search", AgentKey: "worker", TaskID: 101, TS: time.Now().UTC()},
		{ServerID: serverID, ServerName: "usage-test", ToolName: "search", AgentKey: "planner", TaskID: 102, TS: time.Now().UTC()},
		{ServerID: serverID, ServerName: "usage-test", ToolName: "lookup", AgentKey: "worker", TaskID: 101, TS: time.Now().UTC()},
	} {
		if err := d.InsertMCPUsage(usage); err != nil {
			t.Fatal(err)
		}
	}
	serverStats, err := d.MCPServerUsageStats()
	if err != nil {
		t.Fatal(err)
	}
	if got := serverStats[serverID]; got.Calls != 3 || got.Tasks != 2 || len(got.Agents) != 2 {
		t.Fatalf("unexpected server stats: %+v", got)
	}
	toolStats, err := d.MCPToolUsageStats(serverID)
	if err != nil {
		t.Fatal(err)
	}
	if len(toolStats) != 2 || toolStats[0].ToolName != "search" || toolStats[0].Calls != 2 {
		t.Fatalf("unexpected tool stats: %+v", toolStats)
	}
	calls, err := d.RecentMCPCalls(serverID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].ToolName == "" || calls[0].TaskID == 0 {
		t.Fatalf("unexpected recent calls: %+v", calls)
	}
}
