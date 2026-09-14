package db

import (
	"context"
	"encoding/json"
)

// Cursors are PostgreSQL visibility snapshots, not timestamps or sequence IDs:
// a transaction that commits after a page was read must still be unread.
type TaskNotificationQuery struct {
	TaskID     string `json:"task_id"`
	Findings   string `json:"findings"`
	Assets     string `json:"assets"`
	Intercepts string `json:"intercepts"`
}

type TaskNotificationCounts struct {
	TaskID     string `json:"task_id"`
	Findings   int    `json:"findings"`
	Assets     int    `json:"assets"`
	Intercepts int    `json:"intercepts"`
}

type TaskNotificationSummary struct {
	Snapshot   string                   `json:"snapshot"`
	ObservedAt int64                    `json:"observed_at"`
	Items      []TaskNotificationCounts `json:"items"`
}

// One statement and one visibility snapshot for the entire visible task page.
// No payloads, inherited assets, or per-task application queries are loaded.
func (d *DB) TaskNotifications(ctx context.Context, queries []TaskNotificationQuery, findingsOnly bool) (*TaskNotificationSummary, error) {
	input, err := json.Marshal(queries)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = d.QueryRowContext(ctx, taskNotificationsSQL, string(input), findingsOnly).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var result TaskNotificationSummary
	err = json.Unmarshal(raw, &result)
	return &result, err
}

const taskNotificationsSQL = `WITH requested AS MATERIALIZED (
 SELECT q.* FROM jsonb_to_recordset($1::jsonb) AS q(task_id text,findings text,assets text,intercepts text)
 JOIN tasks t ON t.id=q.task_id::bigint WHERE t.deleted_at IS NULL
), finding_counts AS (
 SELECT q.task_id,count(*) n FROM requested q JOIN findings f ON f.task_id=q.task_id::bigint
 WHERE NULLIF(q.findings,'') IS NOT NULL AND f.notification_xid IS NOT NULL
 AND NOT pg_visible_in_snapshot(f.notification_xid,NULLIF(q.findings,'')::pg_snapshot) GROUP BY q.task_id
), asset_groups AS MATERIALIZED (
 SELECT q.task_id,task_asset_host(a) host,
 bool_or(l.notification_xid IS NULL OR
 pg_visible_in_snapshot(l.notification_xid,NULLIF(q.assets,'')::pg_snapshot)) seen_before,
 array_agg(a.id) asset_ids
 FROM requested q JOIN task_asset_links l ON l.task_id=q.task_id::bigint JOIN assets a ON a.id=l.asset_id
 WHERE NOT $2 AND NULLIF(q.assets,'') IS NOT NULL AND a.type IN ('root_domain','subdomain','ip')
 GROUP BY q.task_id,task_asset_host(a),q.assets
), new_asset_groups AS MATERIALIZED (
 SELECT * FROM asset_groups WHERE host<>'' AND NOT seen_before
), asset_counts AS (
 SELECT task_id,count(*) n FROM new_asset_groups
 WHERE (SELECT max(CASE task_asset_effective_approval_state(task_id::bigint,id)
 WHEN 'blocked' THEN 3 WHEN 'revoked' THEN 2 WHEN 'pending' THEN 1 ELSE 0 END)
 FROM unnest(asset_ids) id)=1 GROUP BY task_id
), intercept_counts AS (
 SELECT q.task_id,count(*) n FROM requested q JOIN intercept_pending p ON p.task_id=q.task_id
 WHERE NOT $2 AND NULLIF(q.intercepts,'') IS NOT NULL AND p.status='pending' AND p.notification_xid IS NOT NULL
 AND NOT pg_visible_in_snapshot(p.notification_xid,NULLIF(q.intercepts,'')::pg_snapshot) GROUP BY q.task_id
)
SELECT jsonb_build_object('snapshot',pg_current_snapshot()::text,
 'observed_at',floor(extract(epoch FROM statement_timestamp())*1000000)::bigint,
 'items',COALESCE((SELECT jsonb_agg(jsonb_build_object('task_id',q.task_id,
 'findings',COALESCE(f.n,0),'assets',COALESCE(a.n,0),'intercepts',COALESCE(i.n,0)))
 FROM requested q LEFT JOIN finding_counts f USING(task_id) LEFT JOIN asset_counts a USING(task_id)
 LEFT JOIN intercept_counts i USING(task_id)),'[]'::jsonb))`
