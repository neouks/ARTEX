package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestShellToolNote(t *testing.T) {
	rows := []*db.Tool{
		{
			Key: "nmap", Kind: "shell", Enabled: true, Agents: []string{"worker"}, Description: " network scanner\nwith service detection ",
			Directory: "/opt/nmap/bin", UsageHelp: "nmap [options] target\n--script vuln target", WhenToUse: "discover ports and services",
		},
		{Key: "ffuf", Kind: "shell", Enabled: true, Agents: []string{"worker"}, Description: "content discovery"},
		{Key: "disabled", Kind: "shell", Enabled: false, Agents: []string{"worker"}, Directory: "/disabled"},
		{Key: "planner_only", Kind: "shell", Enabled: true, Agents: []string{"planner"}, Directory: "/planner"},
		{Key: "command_tool", Kind: "command", Enabled: true, Agents: []string{"worker"}, Directory: "/command"},
	}

	note := shellToolNote(rows, "worker")
	want := "\n\n以下工具已安装在此 bash 环境中，可直接通过 Bash 调用：\n" +
		"- nmap: network scanner\n" +
		"  with service detection\n" +
		"  所在目录：/opt/nmap/bin\n" +
		"  用法帮助：nmap [options] target\n" +
		"    --script vuln target\n" +
		"  何时调用：discover ports and services\n" +
		"- ffuf: content discovery"
	if note != want {
		t.Fatalf("shell note mismatch\n got: %q\nwant: %q", note, want)
	}
	for _, unexpected := range []string{"disabled", "planner_only", "command_tool", "所在目录：\n", "用法帮助：\n", "何时调用：\n"} {
		if strings.Contains(note, unexpected) {
			t.Errorf("shell note contains %q: %s", unexpected, note)
		}
	}
	if got := shellToolNote(rows, "unbound"); got != "" {
		t.Fatalf("unbound agent received shell hints: %q", got)
	}
}

func TestCustomToolAgentSchemaIncludesShellMetadata(t *testing.T) {
	schema := customToolSchema("tool key")
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("custom tool schema has no properties: %#v", schema)
	}
	for _, key := range []string{"directory", "usage_help", "when_to_use"} {
		prop, ok := props[key].(map[string]any)
		if !ok || prop["type"] != "string" {
			t.Errorf("%s schema = %#v, want string", key, props[key])
		}
		if !strings.Contains(prop["description"].(string), "shell") {
			t.Errorf("%s description does not state shell-only behavior: %#v", key, prop)
		}
	}

	tool := toDBTool(customToolToolInput{
		Key: "nmap", Kind: "shell", Exec: json.RawMessage(`{}`),
		Directory: "/opt/nmap", UsageHelp: "nmap target", WhenToUse: "scan ports",
	})
	if tool.Directory != "/opt/nmap" || tool.UsageHelp != "nmap target" || tool.WhenToUse != "scan ports" {
		t.Fatalf("agent tool input lost shell metadata: %+v", tool)
	}
}
