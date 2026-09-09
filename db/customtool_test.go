package db

import (
	"encoding/json"
	"testing"
)

// TestCustomToolCRUD exercises create/list/update/delete of a user-defined tool
// (system=false) with kind/exec/deferred. Skips when no Postgres is configured.
func TestCustomToolCRUD(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	key := "ct_test_tool"
	_ = d.DeleteCustomTool(key) // clean slate

	in := &Tool{
		Key:         key,
		Description: "test",
		Schema:      json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`),
		Agents:      []string{"worker"},
		Enabled:     true,
		Kind:        "script",
		Exec:        json.RawMessage(`{"code":"print(1)"}`),
		Deferred:    true,
	}
	if err := d.CreateCustomTool(in); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = d.DeleteCustomTool(key) })

	got, err := d.GetTool(key)
	if err != nil || got == nil {
		t.Fatalf("get after create: %v (nil=%v)", err, got == nil)
	}
	if got.System || got.Kind != "script" || !got.Deferred || len(got.Agents) != 1 {
		t.Fatalf("unexpected row: %+v", got)
	}

	// custom tools appear in ListCustomTools, not among system-only.
	customs, err := d.ListCustomTools()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range customs {
		if c.Key == key {
			found = true
			if c.System {
				t.Fatal("custom tool should have system=false")
			}
		}
	}
	if !found {
		t.Fatal("created tool not in ListCustomTools")
	}

	// update: change kind + disable + rebind.
	in.Kind = "command"
	in.Exec = json.RawMessage(`{"command":"echo hi"}`)
	in.Enabled = false
	in.Agents = []string{"worker", "planner"}
	in.Deferred = false
	if err := d.UpdateCustomTool(in); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := d.GetTool(key)
	if got2.Kind != "command" || got2.Enabled || got2.Deferred || len(got2.Agents) != 2 {
		t.Fatalf("update not applied: %+v", got2)
	}

	// delete only removes non-system rows.
	if err := d.DeleteCustomTool(key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if g, _ := d.GetTool(key); g != nil {
		t.Fatal("tool still present after delete")
	}
}

func TestCustomToolShellMetadata(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	key := "ct_test_shell_metadata"
	_ = d.DeleteCustomTool(key)
	t.Cleanup(func() { _ = d.DeleteCustomTool(key) })

	in := &Tool{
		Key:         key,
		Description: "network scanner",
		Schema:      json.RawMessage(`{}`),
		Agents:      []string{"worker"},
		Enabled:     true,
		Kind:        "shell",
		Exec:        json.RawMessage(`{}`),
		Directory:   "  /opt/nmap/bin  ",
		UsageHelp:   "  nmap [options] target  ",
		WhenToUse:   "  discover ports and services  ",
	}
	if err := d.CreateCustomTool(in); err != nil {
		t.Fatalf("create shell tool: %v", err)
	}
	got, err := d.GetTool(key)
	if err != nil || got == nil {
		t.Fatalf("get shell tool: %v (nil=%v)", err, got == nil)
	}
	if got.Directory != "/opt/nmap/bin" || got.UsageHelp != "nmap [options] target" || got.WhenToUse != "discover ports and services" {
		t.Fatalf("shell metadata did not round-trip trimmed: %+v", got)
	}

	// The DB boundary must clear stale shell guidance regardless of which API path
	// changes the tool into an independently executable kind.
	in.Kind = "command"
	in.Exec = json.RawMessage(`{"command":"nmap"}`)
	if err := d.UpdateCustomTool(in); err != nil {
		t.Fatalf("change shell tool to command: %v", err)
	}
	got, err = d.GetTool(key)
	if err != nil || got == nil {
		t.Fatalf("get command tool: %v (nil=%v)", err, got == nil)
	}
	if got.Directory != "" || got.UsageHelp != "" || got.WhenToUse != "" {
		t.Fatalf("non-shell tool retained shell metadata: %+v", got)
	}
}

func TestShellMetadataNormalization(t *testing.T) {
	directory, usageHelp, whenToUse := shellMetadata(&Tool{
		Kind: "shell", Directory: " /opt/tool ", UsageHelp: " tool --help ", WhenToUse: " when needed ",
	})
	if directory != "/opt/tool" || usageHelp != "tool --help" || whenToUse != "when needed" {
		t.Fatalf("shell metadata not trimmed: %q, %q, %q", directory, usageHelp, whenToUse)
	}
	directory, usageHelp, whenToUse = shellMetadata(&Tool{
		Kind: "script", Directory: "/opt/tool", UsageHelp: "tool --help", WhenToUse: "when needed",
	})
	if directory != "" || usageHelp != "" || whenToUse != "" {
		t.Fatalf("non-shell metadata not cleared: %q, %q, %q", directory, usageHelp, whenToUse)
	}
}
