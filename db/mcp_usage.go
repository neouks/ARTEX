package db

import (
	"database/sql"
	"sort"
	"strings"
	"time"
)

// MCPUsage is one dynamic MCP tool invocation. Arguments and results are not
// retained; the ledger only carries attribution dimensions and a server-name
// snapshot so history remains readable after a server is renamed or deleted.
type MCPUsage struct {
	ServerID      int64     `json:"server_id"`
	ServerName    string    `json:"server_name"`
	ToolName      string    `json:"tool_name"`
	AgentKey      string    `json:"agent_key"`
	TaskID        int64     `json:"task_id"`
	ExplorationID int64     `json:"exploration_id"`
	IntentID      int64     `json:"intent_id"`
	SessionID     string    `json:"session_id"`
	TS            time.Time `json:"ts"`
}

func (d *DB) InsertMCPUsage(u *MCPUsage) error {
	var ts any
	if u != nil && !u.TS.IsZero() {
		ts = u.TS
	}
	_, err := d.Exec(`
	INSERT INTO mcp_usage(ts,server_id,server_name,tool_name,agent_key,task_id,exploration_id,intent_id,session_id)
	VALUES (COALESCE($1,now()),NULLIF($2,0),$3,$4,NULLIF($5,''),$6,$7,$8,NULLIF($9,''))`, ts,
		u.ServerID, u.ServerName, u.ToolName, u.AgentKey, nullIfZero(u.TaskID),
		nullIfZero(u.ExplorationID), nullIfZero(u.IntentID), u.SessionID)
	return err
}

type MCPUsageStat struct {
	ServerID   int64      `json:"server_id"`
	ServerName string     `json:"server_name"`
	ToolName   string     `json:"tool_name,omitempty"`
	Calls      int        `json:"calls"`
	Tasks      int        `json:"tasks"`
	Agents     []string   `json:"agents"`
	LastUsed   *time.Time `json:"last_used,omitempty"`
}

type MCPCall struct {
	TS        time.Time `json:"ts"`
	ToolName  string    `json:"tool_name"`
	AgentKey  string    `json:"agent_key"`
	TaskID    int64     `json:"task_id"`
	SessionID string    `json:"session_id"`
}

func (d *DB) MCPServerUsageStats() (map[int64]MCPUsageStat, error) {
	rows, err := d.Query(`SELECT COALESCE(server_id,0),MAX(server_name),COUNT(*) calls,
COUNT(DISTINCT task_id) FILTER (WHERE task_id IS NOT NULL) tasks,
COALESCE(STRING_AGG(DISTINCT agent_key, ','),''),MAX(ts)
FROM mcp_usage GROUP BY server_id ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]MCPUsageStat{}
	for rows.Next() {
		var st MCPUsageStat
		var agents string
		var last sql.NullTime
		if err := rows.Scan(&st.ServerID, &st.ServerName, &st.Calls, &st.Tasks, &agents, &last); err != nil {
			return nil, err
		}
		st.Agents = splitAgents(agents)
		if last.Valid {
			t := last.Time
			st.LastUsed = &t
		}
		mergeMCPStat(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	for _, aggregate := range archived {
		// One archive represents one task. Aggregate its tool rows by server first
		// so service-level task counts are not added once per tool.
		byServer := map[int64]MCPUsageStat{}
		for _, st := range aggregate.MCPStats {
			current := byServer[st.ServerID]
			current.ServerID = st.ServerID
			if st.ServerName != "" {
				current.ServerName = st.ServerName
			}
			current.Calls += st.Calls
			if st.Tasks > current.Tasks {
				current.Tasks = st.Tasks
			}
			current.Agents = mergeAgentKeys(current.Agents, st.Agents)
			if st.LastUsed != nil && (current.LastUsed == nil || st.LastUsed.After(*current.LastUsed)) {
				current.LastUsed = st.LastUsed
			}
			byServer[st.ServerID] = current
		}
		for _, st := range byServer {
			mergeMCPStat(out, st)
		}
	}
	return out, nil
}

func mergeMCPStat(out map[int64]MCPUsageStat, incoming MCPUsageStat) {
	current := out[incoming.ServerID]
	current.ServerID = incoming.ServerID
	if current.ServerName == "" {
		current.ServerName = incoming.ServerName
	}
	current.Calls += incoming.Calls
	current.Tasks += incoming.Tasks
	current.Agents = mergeAgentKeys(current.Agents, incoming.Agents)
	if incoming.LastUsed != nil && (current.LastUsed == nil || incoming.LastUsed.After(*current.LastUsed)) {
		current.LastUsed = incoming.LastUsed
	}
	out[incoming.ServerID] = current
}

func splitAgents(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	agents := strings.Split(raw, ",")
	sort.Strings(agents)
	return agents
}

func (d *DB) MCPToolUsageStats(serverID int64) ([]MCPUsageStat, error) {
	rows, err := d.Query(`SELECT server_id,MAX(server_name),tool_name,COUNT(*) calls,
COUNT(DISTINCT task_id) FILTER (WHERE task_id IS NOT NULL) tasks,
COALESCE(STRING_AGG(DISTINCT agent_key, ','),''),MAX(ts)
FROM mcp_usage WHERE server_id=$1 GROUP BY server_id,tool_name ORDER BY COUNT(*) DESC,tool_name`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPUsageStat{}
	for rows.Next() {
		var st MCPUsageStat
		var agents string
		var last sql.NullTime
		if err := rows.Scan(&st.ServerID, &st.ServerName, &st.ToolName, &st.Calls, &st.Tasks, &agents, &last); err != nil {
			return nil, err
		}
		st.Agents = splitAgents(agents)
		if last.Valid {
			t := last.Time
			st.LastUsed = &t
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	byTool := map[string]MCPUsageStat{}
	for _, st := range out {
		byTool[st.ToolName] = st
	}
	for _, aggregate := range archived {
		for _, st := range aggregate.MCPStats {
			if st.ServerID != serverID {
				continue
			}
			current := byTool[st.ToolName]
			current.ServerID, current.ServerName, current.ToolName = serverID, st.ServerName, st.ToolName
			current.Calls += st.Calls
			current.Tasks += st.Tasks
			current.Agents = mergeAgentKeys(current.Agents, st.Agents)
			if st.LastUsed != nil && (current.LastUsed == nil || st.LastUsed.After(*current.LastUsed)) {
				current.LastUsed = st.LastUsed
			}
			byTool[st.ToolName] = current
		}
	}
	out = out[:0]
	for _, st := range byTool {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].ToolName < out[j].ToolName
	})
	return out, nil
}

func (d *DB) RecentMCPCalls(serverID int64, limit int) ([]MCPCall, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.Query(`SELECT ts,tool_name,COALESCE(agent_key,''),COALESCE(task_id,0),COALESCE(session_id,'')
FROM mcp_usage WHERE server_id=$1 ORDER BY ts DESC,id DESC LIMIT $2`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPCall{}
	for rows.Next() {
		var c MCPCall
		if err := rows.Scan(&c.TS, &c.ToolName, &c.AgentKey, &c.TaskID, &c.SessionID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
