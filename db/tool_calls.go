package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrToolCallRange = errors.New("invalid tool call detail range")

// ToolCallScope is constructed by HTTP session authorization, never from a cursor.
type ToolCallScope struct {
	ExplorationID  int64
	ConversationID int64
	Session        ActivitySessionFilter
}

type ToolCallQuery struct {
	Q, Type, Status  string
	BuiltinNames     []string // names from executable code, not historical guesses
	Before, Snapshot int64
	Limit            int
	Running          bool
}

type ToolCall struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UseID     *int64    `json:"use_id"`
	ResultID  *int64    `json:"result_id"`
	ToolUseID string    `json:"tool_use_id"`
}

type ToolCallPage struct {
	Items          []ToolCall `json:"items"`
	HasMore        bool       `json:"has_more"`
	SnapshotCursor int64      `json:"snapshot_cursor"`
	NextCursor     string     `json:"next_cursor"`
	SourceTaskID   int64      `json:"source_task_id,omitempty"`
	ReadOnly       bool       `json:"read_only"`
}

type ToolCallText struct {
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	Total      int    `json:"total"`
	HasMore    bool   `json:"has_more"`
}

func (s ToolCallScope) sql() (table, where, partition string, args []any, err error) {
	switch {
	case s.ExplorationID > 0 && s.ConversationID == 0:
		cond, values := s.Session.cond(2)
		return "activity", "exploration_id=$1" + cond, "COALESCE(worker,''),COALESCE(node_id,0),COALESCE(main_seg,0),tool_use_id", append([]any{s.ExplorationID}, values...), nil
	case s.ConversationID > 0 && s.ExplorationID == 0:
		return "conversation_activities", "conversation_id=$1", "COALESCE(worker,''),tool_use_id", []any{s.ConversationID}, nil
	default:
		return "", "", "", nil, fmt.Errorf("invalid tool call scope")
	}
}

func (s *ExplorationStore) ToolCallScope(f ActivitySessionFilter) ToolCallScope {
	return ToolCallScope{ExplorationID: s.expID, Session: f}
}

// ListToolCalls pairs BEFORE filtering/pagination. It projects only metadata;
// neither large arguments nor results are read by this query. A repeated use ID
// belongs to its nearest preceding invocation in the same execution session.
func (d *DB) ListToolCalls(ctx context.Context, scope ToolCallScope, q ToolCallQuery) (ToolCallPage, error) {
	page := ToolCallPage{Items: []ToolCall{}}
	if q.Limit < 1 || q.Limit > 50 || q.Before < 0 || q.Snapshot < 0 {
		return page, fmt.Errorf("invalid tool call page")
	}
	table, where, partition, args, err := scope.sql()
	if err != nil {
		return page, err
	}
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	before, snapshot := bind(q.Before), bind(q.Snapshot)
	search, typ, status, running, limit := bind(q.Q), bind(q.Type), bind(q.Status), bind(q.Running), bind(q.Limit+1)
	builtins := bind(q.BuiltinNames)
	extraCols := ""
	if scope.ExplorationID > 0 {
		extraCols = ",node_id,main_seg"
	}
	// Only the small relevant activity stream enters the window. The latest
	// session terminal result prevents old unfinished calls being marked running.
	sql := `WITH bounds AS (
	 SELECT COALESCE(max(id),0) AS latest, COALESCE(max(id) FILTER (WHERE kind IN ('result','round','user')),0) AS terminal FROM ` + table + ` WHERE ` + where + `
	), epochs AS (
	 SELECT *, COALESCE(max(id) FILTER (WHERE kind IN ('result','round','user')) OVER
	 (PARTITION BY ` + strings.TrimSuffix(partition, ",tool_use_id") + ` ORDER BY id ROWS UNBOUNDED PRECEDING),0) AS epoch
	 FROM (SELECT id,kind,tool,tool_use_id,is_error,created_at,worker` + extraCols + `
	 FROM ` + table + ` WHERE ` + where + ` AND kind IN ('tool_use','tool_result','result','round','user')) scoped
	), events AS (
	 SELECT id,kind,tool,tool_use_id,is_error,created_at,
	 max(id) FILTER (WHERE kind='tool_use') OVER (PARTITION BY ` + partition + `,epoch ORDER BY id ROWS UNBOUNDED PRECEDING) AS use_id
	 FROM epochs WHERE kind IN ('tool_use','tool_result')
	), grouped AS (
	 SELECT CASE WHEN COALESCE(tool_use_id,'')<>'' THEN COALESCE(use_id,id) ELSE id END AS anchor,
	 max(id) FILTER (WHERE kind='tool_use') AS input_id,
	 max(id) FILTER (WHERE kind='tool_result') AS output_id,
	 COALESCE(NULLIF(max(tool) FILTER (WHERE kind='tool_use'),''),(array_agg(tool ORDER BY id DESC) FILTER (WHERE kind='tool_result'))[1],'') AS name,
	 COALESCE(max(tool_use_id),'') AS call_id, min(created_at) AS created_at,
	 (array_agg(is_error ORDER BY id DESC) FILTER (WHERE kind='tool_result'))[1] AS is_error
	 FROM events GROUP BY 1
	), calls AS (
	 SELECT g.anchor,g.input_id,g.output_id,g.name,g.call_id,g.created_at,
	 CASE WHEN g.output_id IS NOT NULL THEN CASE WHEN g.is_error THEN 'failed' ELSE 'success' END
	 WHEN ` + running + ` AND g.anchor>b.terminal THEN 'running' ELSE 'missing' END AS status,
	 CASE WHEN g.name=ANY(` + builtins + `::text[]) THEN 'builtin' WHEN t.system=false THEN 'custom'
	 WHEN g.name ~ '^mcp__[^_].*__.+$' THEN 'mcp' ELSE 'unknown' END AS type
	 FROM grouped g LEFT JOIN tools t ON t.key=g.name CROSS JOIN bounds b
	 WHERE g.anchor<=CASE WHEN ` + snapshot + `::bigint=0 THEN b.latest ELSE ` + snapshot + ` END
	 AND (` + before + `::bigint=0 OR g.anchor<` + before + `)
	), selected AS (
	 SELECT * FROM calls WHERE (` + search + `='' OR strpos(lower(name),lower(` + search + `))>0)
	 AND (` + typ + `='' OR type=` + typ + `) AND (` + status + `='' OR status=` + status + `)
	 ORDER BY anchor DESC LIMIT ` + limit + `
	) SELECT b.latest,s.anchor,s.input_id,s.output_id,s.name,s.call_id,s.created_at,s.status,s.type
	 FROM bounds b LEFT JOIN selected s ON true ORDER BY s.anchor DESC`
	rows, err := d.QueryContext(ctx, sql, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, in, out *int64
		var name, callID, state, kind *string
		var created *time.Time
		if err = rows.Scan(&page.SnapshotCursor, &id, &in, &out, &name, &callID, &created, &state, &kind); err != nil {
			return page, err
		}
		if id != nil {
			page.Items = append(page.Items, ToolCall{ID: *id, UseID: in, ResultID: out, Name: *name, ToolUseID: *callID, CreatedAt: *created, Status: *state, Type: *kind})
		}
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > q.Limit {
		page.HasMore = true
		page.Items = page.Items[:q.Limit]
	}
	return page, nil
}

// ToolCallDetail never reads another session and slices in Unicode characters in
// PostgreSQL, rather than loading/truncating a multi-megabyte body in the server.
func (d *DB) ToolCallDetail(ctx context.Context, scope ToolCallScope, id int64, offset, limit int) (ToolCallText, error) {
	res := ToolCallText{Offset: offset}
	if id <= 0 || offset < 0 || limit < 1 || limit > 24000 {
		return res, ErrToolCallRange
	}
	table, where, _, args, err := scope.sql()
	if err != nil {
		return res, err
	}
	n := len(args)
	args = append(args, id, offset+1, limit)
	body := `COALESCE(NULLIF(detail,''),summary,'')`
	err = d.QueryRowContext(ctx, fmt.Sprintf(`SELECT substring(%s from $%d::integer for $%d::integer),char_length(%s) FROM %s WHERE %s AND id=$%d AND kind IN ('tool_use','tool_result')`, body, n+2, n+3, body, table, where, n+1), args...).Scan(&res.Text, &res.Total)
	if err != nil {
		return res, err
	}
	if offset > res.Total {
		return res, ErrToolCallRange
	}
	res.NextOffset = min(offset+limit, res.Total)
	res.HasMore = res.NextOffset < res.Total
	return res, nil
}
