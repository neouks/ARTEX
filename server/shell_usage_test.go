package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func TestShellUsageCommands(t *testing.T) {
	rows := []*db.Tool{{Key: "scan", Executable: "nmap", Kind: "shell", Enabled: true, Agents: []string{"worker"}}, {Key: "fetch", Executable: "/opt/bin/curl", Kind: "shell", Enabled: true, Agents: []string{"worker"}}, {Key: "hidden", Executable: "ffuf", Kind: "shell", Enabled: true, Agents: []string{"planner"}}}
	for _, tt := range []struct {
		command string
		want    []string
	}{
		{"nmap a; nmap b | /opt/bin/curl x", []string{"fetch", "scan"}},
		{"X=1 env -u HOME A=b sudo -u root command -- nmap a", []string{"scan"}},
		{"echo nmap; # nmap a\n echo 'nmap b'; f() { nmap c; }; bash -c 'nmap d'", nil},
		{"$tool a; ${tool} b; $(printf nmap) c", nil},
		{"false && nmap a", []string{"scan"}},
		{"'/opt/bin/curl' x; command -v nmap; ffuf x", []string{"fetch"}},
		{"nmap '$(bad)'", []string{"scan"}},
		{"nmap '", nil},
	} {
		t.Run(tt.command, func(t *testing.T) {
			got, _ := shellUsageKeys(tt.command, rows, "worker")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
	rows = append(rows, &db.Tool{Key: "duplicate", Executable: "nmap", Kind: "shell", Enabled: true, Agents: []string{"worker"}})
	if got, conflicts := shellUsageKeys("nmap a", rows, "worker"); len(got) != 0 || len(conflicts) != 1 {
		t.Fatal(got, conflicts)
	}
}
func TestShellUsageMeterAndFailure(t *testing.T) {
	recorder := &recordingToolUsage{}
	base := actool.Build(actool.Spec{Name: "Bash", Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		return actool.Errorf("failed command"), nil
	}})
	ri := agent.RunInfo{TaskID: 7, ExplorationID: 8, IntentID: 9, SessionID: "s"}
	tool := &shellMeteredTool{CoreTool: meterTool(base, recorder, "Bash", "worker", ri), recorder: recorder, declarations: []*db.Tool{{Key: "nmap", Kind: "shell", Enabled: true, Agents: []string{"worker"}}}, agentKey: "worker", ri: ri}
	r, e := tool.Call(t.Context(), json.RawMessage(`{"command":"nmap a; nmap b"}`), nil)
	if e != nil || !r.IsError || len(recorder.rows) != 2 {
		t.Fatal(r, e, recorder.rows)
	}
	if recorder.rows[0].ToolKey != "nmap" || recorder.rows[0].SessionID != "s" || recorder.rows[1].ToolKey != "Bash" {
		t.Fatal(recorder.rows)
	}
}

func TestCustomToolDirectAndDeferredUsage(t *testing.T) {
	t.Setenv("AGENT_CORE_DISABLE_INTERACTIVE_SHELL", "1")
	s, _ := modeServer(t)
	old := agent.ToolResolve
	oldFinding := agent.FindingTrafficBindingEnabled
	defer func() { agent.ToolResolve = old; agent.FindingTrafficBindingEnabled = oldFinding }()
	wireTools(s.m.pg, nil)
	for _, kind := range []string{"command", "script", "http"} {
		key := "usage_probe_" + kind
		s.m.pg.DeleteCustomTool(key)
		if err := s.m.pg.CreateCustomTool(&db.Tool{Key: key, Kind: kind, Enabled: true, Deferred: true, Agents: []string{"worker"}, Description: "decorated"}); err != nil {
			t.Fatal(err)
		}
		defer s.m.pg.DeleteCustomTool(key)
		defer s.m.pg.Exec(`DELETE FROM tool_usage WHERE tool_key=$1`, key)
		calls := 0
		base := actool.Build(actool.Spec{Name: key, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			calls++
			return actool.Text("ok"), nil
		}})
		resolved, err := agent.ToolResolve(t.Context(), "worker", []actool.CoreTool{base})
		if err != nil || len(resolved) != 1 {
			t.Fatal(err, resolved)
		}
		if _, err := resolved[0].Call(t.Context(), json.RawMessage(`{}`), nil); err != nil {
			t.Fatal(err)
		}
		reg := actool.NewRegistry(resolved[0])
		extra := actool.NewExecuteExtraTool(reg, nil)
		raw, _ := json.Marshal(map[string]any{"tool_name": key, "params": map[string]any{}})
		result, err := extra.Call(t.Context(), raw, &actool.ToolContext{ExecuteTool: func(ctx context.Context, name string, input json.RawMessage) (actool.Result, error) {
			return resolved[0].Call(ctx, input, nil)
		}})
		if err != nil || result.IsError {
			t.Fatal(result, err)
		}
		counts, err := s.m.pg.ToolUsageCounts()
		if err != nil || counts[key] != 2 || calls != 2 {
			t.Fatal(counts[key], calls, err)
		}
		// Missing policy dispatcher must not reach the metered inner tool.
		extra.Call(t.Context(), raw, nil)
		counts, _ = s.m.pg.ToolUsageCounts()
		if counts[key] != 2 {
			t.Fatal("denied deferred call counted")
		}
	}
}
