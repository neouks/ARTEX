package db

import (
	"fmt"
	"testing"
)

func TestToolUsageBackfill(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("tool usage migration", "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := d.CreateConversation("mainagent", "tool usage migration", nil)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("backfill_%d", task.ID)
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM tool_usage WHERE tool_key=$1`, key)
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, task.ExplorationID)
		_, _ = d.Exec(`DELETE FROM conversations WHERE id=$1`, conversation.ID)
	})
	// Task has three legacy calls, one already metered. Conversation has two
	// calls and no ledger. Results/text must never count as additional calls.
	for _, kind := range []string{"tool_use", "tool_use", "tool_use", "tool_result", "text"} {
		if _, err = d.Exec(`INSERT INTO activity(exploration_id,worker,kind,tool) VALUES($1,'planner',$2,$3)`, task.ExplorationID, kind, key); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err = d.Exec(`INSERT INTO conversation_activities(conversation_id,kind,tool) VALUES($1,'tool_use',$2)`, conversation.ID, key); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.InsertToolUsage(&ToolUsage{ToolKey: key, ExplorationID: task.ExplorationID, TaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`DELETE FROM tool_usage_migrations WHERE name='legacy_activity_v1'`); err != nil {
		t.Fatal(err)
	}
	if err = d.backfillToolUsage(); err != nil {
		t.Fatal(err)
	}
	assertCount := func(want int) {
		t.Helper()
		counts, err := d.ToolUsageCounts()
		if err != nil || counts[key] != want {
			t.Fatalf("calls=%d want=%d err=%v", counts[key], want, err)
		}
	}
	assertCount(5)
	// Restart, new metered calls and deletion of original history don't inflate
	// or reduce the migrated lifetime total.
	if err = d.backfillToolUsage(); err != nil {
		t.Fatal(err)
	}
	assertCount(5)
	if err = d.InsertToolUsage(&ToolUsage{ToolKey: key, SessionID: fmt.Sprintf("conv-%d", conversation.ID)}); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`DELETE FROM activity WHERE exploration_id=$1`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if err = d.backfillToolUsage(); err != nil {
		t.Fatal(err)
	}
	assertCount(6)
}

func TestToolUsageLedger(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	const (
		toolA = "zz_test_tool_usage_a"
		toolB = "zz_test_tool_usage_b"
	)
	cleanup := func() {
		_, _ = d.Exec(`DELETE FROM tool_usage WHERE tool_key IN ($1,$2)`, toolA, toolB)
	}
	cleanup()
	defer cleanup()

	rows := []*ToolUsage{
		{ToolKey: toolA, AgentKey: "worker", TaskID: 991, ExplorationID: 5, IntentID: 7},
		{ToolKey: toolA, AgentKey: "planner", TaskID: 992, ExplorationID: 6},
		{ToolKey: toolA, AgentKey: "chatbot", SessionID: "conv-1"},
		{ToolKey: toolB, AgentKey: "worker", TaskID: 991},
	}
	for _, row := range rows {
		if err := d.InsertToolUsage(row); err != nil {
			t.Fatalf("insert %s: %v", row.ToolKey, err)
		}
	}

	counts, err := d.ToolUsageCounts()
	if err != nil {
		t.Fatal(err)
	}
	if got := counts[toolA]; got != 3 {
		t.Errorf("%s calls: want 3, got %d", toolA, got)
	}
	if got := counts[toolB]; got != 1 {
		t.Errorf("%s calls: want 1, got %d", toolB, got)
	}
}
