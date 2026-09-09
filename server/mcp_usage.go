package server

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

type meteredMCPTool struct {
	actool.CoreTool
	recorder   interface{ InsertMCPUsage(*db.MCPUsage) error }
	serverID   int64
	serverName string
	toolName   string
	agentKey   string
	ri         agent.RunInfo
}

func meterMCPTool(t actool.CoreTool, recorder interface{ InsertMCPUsage(*db.MCPUsage) error }, serverID int64, serverName, agentKey string, ri agent.RunInfo) actool.CoreTool {
	if recorder == nil {
		return t
	}
	name := strings.TrimPrefix(t.Name(), "mcp__"+serverName+"__")
	return &meteredMCPTool{CoreTool: t, recorder: recorder, serverID: serverID, serverName: serverName, toolName: name, agentKey: agentKey, ri: ri}
}

func (m *meteredMCPTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if err := m.recorder.InsertMCPUsage(&db.MCPUsage{ServerID: m.serverID, ServerName: m.serverName, ToolName: m.toolName, AgentKey: m.agentKey, TaskID: m.ri.TaskID, ExplorationID: m.ri.ExplorationID, IntentID: m.ri.IntentID, SessionID: m.ri.SessionID}); err != nil {
		log.Printf("[mcusage] insert %s/%s: %v", m.serverName, m.toolName, err)
	}
	return m.CoreTool.Call(ctx, input, tc)
}
