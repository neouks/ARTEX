package server

import (
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
	"mvdan.cc/sh/v3/syntax"
)

// Counts submitted command requests, not processes or iterations. Never expands
// variables, evaluates substitutions, or inspects scripts / bash -c strings.
func shellCommands(command string) []string {
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	names := map[string]bool{}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch node := n.(type) {
		case *syntax.FuncDecl:
			return false
		case *syntax.CallExpr:
			args := []string{}
			for _, word := range node.Args {
				value, ok := literalShellWord(word)
				if !ok {
					break
				}
				args = append(args, value)
			}
			if cmd := unwrapShellCommand(args); cmd != "" {
				names[cmd] = true
			}
			// Do not inspect substitutions embedded in arguments.
			return false
		}
		return true
	})
	out := []string{}
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
func literalShellWord(w *syntax.Word) (string, bool) {
	var out strings.Builder
	for _, p := range w.Parts {
		switch v := p.(type) {
		case *syntax.Lit:
			// Backslash escapes are deliberately not guessed.
			if strings.Contains(v.Value, "\\") {
				return "", false
			}
			out.WriteString(v.Value)
		case *syntax.SglQuoted:
			out.WriteString(v.Value)
		case *syntax.DblQuoted:
			value, ok := literalShellWord(&syntax.Word{Parts: v.Parts})
			if !ok {
				return "", false
			}
			out.WriteString(value)
		default:
			return "", false
		}
	}
	return out.String(), true
}
func unwrapShellCommand(args []string) string {
	for len(args) > 0 {
		original := args[0]
		name := filepath.Base(original)
		args = args[1:]
		if name != "env" && name != "sudo" && name != "command" {
			return original
		}
		for len(args) > 0 {
			a := args[0]
			if a == "--" {
				args = args[1:]
				break
			}
			if name == "env" && strings.Contains(a, "=") && !strings.HasPrefix(a, "-") {
				args = args[1:]
				continue
			}
			if !strings.HasPrefix(a, "-") {
				break
			}
			args = args[1:]
			switch {
			case name == "command" && (a == "-v" || a == "-V"):
				return ""
			case name == "command" && a == "-p":
			case name == "env" && (a == "-i" || a == "--ignore-environment"):
			case name == "sudo" && (a == "-n" || a == "-E" || a == "-H"):
			case name == "env" && (a == "-u" || a == "--unset" || a == "-C" || a == "--chdir"), name == "sudo" && (a == "-u" || a == "-g" || a == "--user" || a == "--group"):
				if len(args) == 0 {
					return ""
				}
				args = args[1:]
			default:
				return ""
			}
		}
		if len(args) > 0 && filepath.Base(args[0]) != "env" && filepath.Base(args[0]) != "sudo" && filepath.Base(args[0]) != "command" {
			return args[0]
		}
	}
	return ""
}

func shellUsageKeys(command string, rows []*db.Tool, agentKey string) (keys, ambiguous []string) {
	matched := map[string]bool{}
	for _, cmd := range shellCommands(command) {
		candidates := []string{}
		for _, row := range rows {
			if row.System || row.Kind != "shell" || !row.Enabled || !contains(row.Agents, agentKey) {
				continue
			}
			target := row.ShellCommand()
			if (filepath.IsAbs(target) && filepath.Clean(target) == filepath.Clean(cmd)) || (!strings.ContainsAny(target, "/\\") && target == filepath.Base(cmd)) {
				candidates = append(candidates, row.Key)
			}
		}
		if len(candidates) == 1 {
			matched[candidates[0]] = true
		} else if len(candidates) > 1 {
			ambiguous = append(ambiguous, strings.Join(candidates, ", "))
		}
	}
	for k := range matched {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return
}

type shellMeteredTool struct {
	actool.CoreTool
	recorder     toolUsageRecorder
	declarations []*db.Tool
	agentKey     string
	ri           agent.RunInfo
}

func (m *shellMeteredTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	var input struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(in, &input) == nil && ctx.Err() == nil {
		keys, conflicts := shellUsageKeys(input.Command, m.declarations, m.agentKey)
		for _, conflict := range conflicts {
			log.Printf("[toolusage] ambiguous shell declarations: %s", conflict)
		}
		for _, key := range keys {
			if err := m.recorder.InsertToolUsage(&db.ToolUsage{ToolKey: key, AgentKey: m.agentKey, TaskID: m.ri.TaskID, ExplorationID: m.ri.ExplorationID, IntentID: m.ri.IntentID, SessionID: m.ri.SessionID}); err != nil {
				log.Printf("[toolusage] shell %s: %v", key, err)
			}
		}
	}
	return m.CoreTool.Call(ctx, in, tc)
}
