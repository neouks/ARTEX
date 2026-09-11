package db

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type TaskAssetViewQuery struct {
	Status      string `json:"status"`
	Q           string `json:"q"`
	Limit       int    `json:"limit"`
	Cursor      string `json:"cursor"`
	SummaryOnly bool   `json:"summary_only"`
}
type TaskAssetViewRow struct {
	AssetID     *int64 `json:"asset_id"`
	GroupKey    string `json:"group_key"`
	Host        string `json:"host"`
	RecordCount int    `json:"record_count"`
	State       string `json:"approval_state"`
	OwnerTaskID int64  `json:"owner_task_id"`
	ReadOnly    bool   `json:"read_only"`
	CanSchedule bool   `json:"can_schedule"`
}
type TaskAssetView struct {
	View       string             `json:"view"`
	Template   string             `json:"asset_approval_template"`
	Counts     map[string]int     `json:"counts"`
	Total      int                `json:"total"`
	Assets     []TaskAssetViewRow `json:"assets"`
	NextCursor string             `json:"next_cursor,omitempty"`
}
type taskAssetViewCursor struct {
	Binding string `json:"binding"`
	Host    string `json:"host"`
	Owner   int64  `json:"owner"`
}

// RefreshTaskAssetGroups revalidates only identities retained in model history.
// Counts and old page cursors remain snapshots; it does not replay old queries.
func (s *AssetStore) RefreshTaskAssetGroups(taskID int64, keys []string) (map[string]TaskAssetViewRow, error) {
	out := make(map[string]TaskAssetViewRow)
	if len(keys) == 0 {
		return out, nil
	}
	hosts := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, host, ok := strings.Cut(key, "|host:"); ok {
			hosts = append(hosts, host)
		}
	}
	rows, err := s.query(strings.ReplaceAll(taskAssetViewGroupsSQL, "$7", "$4")+` SELECT asset_id,owner::text||'|host:'||host,host,record_count,state,owner,owner<>$1,state='approved' AND asset_id IS NOT NULL
	 FROM grouped WHERE owner::text||'|host:'||host=ANY($3::text[])`, taskID, "", keys, hosts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row TaskAssetViewRow
		if err := rows.Scan(&row.AssetID, &row.GroupKey, &row.Host, &row.RecordCount, &row.State, &row.OwnerTaskID, &row.ReadOnly, &row.CanSchedule); err != nil {
			return nil, err
		}
		out[row.GroupKey] = row
	}
	return out, rows.Err()
}

// QueryTaskAssetView aggregates narrow host records in PostgreSQL. It never
// loads full assets or changes authorization. All counts share one snapshot.
func (s *AssetStore) QueryTaskAssetView(taskID int64, q TaskAssetViewQuery) (*TaskAssetView, error) {
	if taskID <= 0 {
		return nil, ErrTaskAssetTaskNotFound
	}
	if q.Status == "" {
		q.Status = ApprovalApproved
	}
	switch q.Status {
	case "all", ApprovalApproved, ApprovalPending, ApprovalBlocked, ApprovalRevoked:
	default:
		return nil, fmt.Errorf("invalid status %q", q.Status)
	}
	q.Q = strings.TrimSpace(q.Q)
	if len(q.Q) > 253 {
		return nil, fmt.Errorf("q exceeds 253 bytes")
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 {
		return nil, fmt.Errorf("limit must be between 1 and 50")
	}
	bindingBytes, _ := json.Marshal([]any{1, taskID, q.Status, q.Q})
	binding := fmt.Sprintf("%x", sha256.Sum256(bindingBytes))
	cursor := taskAssetViewCursor{}
	if q.Cursor != "" {
		if len(q.Cursor) > 2048 {
			return nil, fmt.Errorf("invalid cursor length")
		}
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Binding != binding || cursor.Host == "" || cursor.Owner <= 0 || q.SummaryOnly {
			return nil, fmt.Errorf("invalid or mismatched cursor")
		}
	}
	limit := q.Limit + 1
	if q.SummaryOnly {
		limit = 0
	}
	var raw []byte
	err := s.queryRow(taskAssetViewSQL, taskID, q.Q, q.Status, cursor.Host, cursor.Owner, limit, nil).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var out TaskAssetView
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Assets) > q.Limit {
		out.Assets = out.Assets[:q.Limit]
		last := out.Assets[len(out.Assets)-1]
		encoded, _ := json.Marshal(taskAssetViewCursor{binding, last.Host, last.OwnerTaskID})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return &out, nil
}

const taskAssetViewGroupsSQL = `WITH candidates AS MATERIALIZED (
 SELECT l.asset_id,l.task_id owner,task_asset_host(a) host,
 task_asset_owner_approval_state(l.task_id,a.id) owner_state,
 task_asset_effective_approval_state($1,a.id) effective_state
 FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
 WHERE a.type IN ('root_domain','subdomain','ip')
 AND ($7::text[] IS NULL OR task_asset_host(a)=ANY($7::text[]))
 AND (l.task_id=$1 OR l.task_id IN(SELECT source_task_id FROM task_relations WHERE task_id=$1))
), chosen AS (
 SELECT DISTINCT ON(asset_id) * FROM candidates
 ORDER BY asset_id,(owner=$1) DESC,CASE owner_state WHEN 'approved' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END,owner
), records AS (
 SELECT asset_id,owner,host,CASE WHEN effective_state<>'approved' THEN effective_state ELSE owner_state END state FROM chosen
 UNION ALL
 SELECT NULL::bigint,$1,b.host_key,'blocked' FROM task_asset_blocks b
 WHERE b.task_id=$1 AND b.asset_type IN ('root_domain','subdomain','ip') AND b.host_key<>''
 AND ($7::text[] IS NULL OR b.host_key=ANY($7::text[]))
 AND NOT EXISTS(SELECT 1 FROM chosen c WHERE c.host=b.host_key)
), grouped AS MATERIALIZED (
 SELECT host,owner,min(asset_id) asset_id,count(DISTINCT asset_id)::int record_count,
 CASE max(CASE state WHEN 'blocked' THEN 3 WHEN 'revoked' THEN 2 WHEN 'pending' THEN 1 ELSE 0 END)
 WHEN 3 THEN 'blocked' WHEN 2 THEN 'revoked' WHEN 1 THEN 'pending' ELSE 'approved' END state
 FROM records WHERE host<>'' AND ($2='' OR strpos(lower(host),lower($2))>0)
 GROUP BY host,owner
)`

const taskAssetViewSQL = taskAssetViewGroupsSQL + `, page AS (
 SELECT * FROM grouped WHERE ($3='all' OR state=$3)
 AND ($4='' OR (host COLLATE "C",owner)>($4 COLLATE "C",$5::bigint))
 ORDER BY host COLLATE "C",owner LIMIT $6
)
SELECT jsonb_build_object('view','approval_management','asset_approval_template',t.asset_approval_template,
 'counts',(SELECT jsonb_build_object('approved',count(*) FILTER(WHERE state='approved'),
 'pending',count(*) FILTER(WHERE state='pending'),'blocked',count(*) FILTER(WHERE state='blocked'),
 'revoked',count(*) FILTER(WHERE state='revoked')) FROM grouped),
 'total',(SELECT count(*) FROM grouped WHERE $3='all' OR state=$3),
 'assets',COALESCE((SELECT jsonb_agg(jsonb_build_object('asset_id',asset_id,
 'group_key',owner::text||'|host:'||host,'host',host,'record_count',record_count,
 'approval_state',state,'owner_task_id',owner,'read_only',owner<>$1,
 'can_schedule',state='approved' AND asset_id IS NOT NULL) ORDER BY host COLLATE "C",owner) FROM page),'[]'::jsonb))
FROM tasks t WHERE t.id=$1 AND t.deleted_at IS NULL`
