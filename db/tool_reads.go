package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type ToolNodePage struct {
	Nodes []*Node `json:"nodes"`
	Total int     `json:"total"`
}

type ToolDigestIndexRow struct {
	ID          int64  `json:"id"`
	Body        string `json:"body"`
	MemberCount int    `json:"member_count"`
}
type ToolDigestIndexPage struct {
	Digests []ToolDigestIndexRow `json:"digests"`
	Total   int                  `json:"total"`
}

func (s *ExplorationStore) ToolDigestIndexPage(ctx context.Context, assetID, before int64, limit int) (ToolDigestIndexPage, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, toolNodeVisibilitySQL+`, selected AS MATERIALIZED (
 SELECT v.id FROM visible v WHERE COALESCE((SELECT a.asset_id FROM exploration_edges e JOIN exploration_anchors a ON a.node_id=e.dst_id
 WHERE e.exploration_id=v.exploration_id AND e.src_id=v.id AND e.rel='covers' GROUP BY a.asset_id ORDER BY count(*) DESC,a.asset_id LIMIT 1),0)=$9
 ), page AS (SELECT id FROM selected WHERE $6::bigint=0 OR id<$6 ORDER BY id DESC LIMIT $7)
 SELECT jsonb_build_object('total',(SELECT count(*) FROM selected),'digests',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',n.id,'body',left(n.payload->>'body',360),
 'member_count',(SELECT count(*) FROM exploration_edges e WHERE e.src_id=n.id AND e.exploration_id=n.exploration_id AND e.rel='covers')) ORDER BY n.id DESC)
 FROM page p JOIN exploration_nodes n ON n.id=p.id),'[]'::jsonb))`, s.expID, KindDigest, "", "", int64(0), before, limit+1, false, assetID).Scan(&raw)
	var out ToolDigestIndexPage
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

// ToolSearchWorkerTraces shares the authorized-node snapshot with fact and
// finding reads; unauthorized rows never consume a result page.
func (s *ExplorationStore) ToolSearchWorkerTraces(ctx context.Context, exclude int64, q string, before int64, limit int) ([]Activity, error) {
	rows, err := s.db.QueryContext(ctx, toolNodeVisibilitySQL+`
 SELECT a.id,a.node_id,COALESCE(a.worker,''),COALESCE(a.kind,''),COALESCE(a.tool,''),a.is_error,left(COALESCE(a.summary,''),100),v.owner,v.exploration_id<>$1
 FROM activity a JOIN visible v ON v.id=a.node_id AND v.exploration_id=a.exploration_id
 WHERE a.kind NOT IN ('thinking','usage') AND a.node_id<>$10 AND ($6::bigint=0 OR a.id<$6)
 AND strpos(lower(COALESCE(a.summary,'')||' '||COALESCE(a.detail,'')),lower($9))>0
 ORDER BY a.id DESC LIMIT $7`, s.expID, KindIntent, "", "", int64(0), before, limit+1, true, q, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind, &a.Tool, &a.IsError, &a.Summary, &a.SourceTaskID, &a.Inherited); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type ToolDigestMembers struct {
	ID      int64   `json:"id"`
	Members []int64 `json:"members"`
	Anchors []int64 `json:"anchors"`
}

func (s *ExplorationStore) ToolDigestMemberships(ids []int64) ([]ToolDigestMembers, error) {
	var raw []byte
	err := s.db.QueryRow(`SELECT COALESCE(jsonb_agg(jsonb_build_object('id',n.id,'members',
 COALESCE((SELECT jsonb_agg(e.dst_id ORDER BY e.dst_id) FROM exploration_edges e WHERE e.exploration_id=n.exploration_id AND e.src_id=n.id AND e.rel='covers'),'[]'::jsonb),
 'anchors',COALESCE(n.payload->'anchor_ids','[]'::jsonb))),'[]'::jsonb)
 FROM exploration_nodes n WHERE n.exploration_id=$1 AND n.kind='digest' AND n.id=ANY($2::bigint[])`, s.expID, ids).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var out []ToolDigestMembers
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (s *AssetStore) ToolAssetsByIDs(taskID int64, ids []int64) ([]*Asset, error) {
	rows, err := s.query(s.selectAssetColumns()+` WHERE id=ANY($1::bigint[]) AND ($2::bigint=0 OR task_asset_effectively_approved($2,id)) ORDER BY id DESC`, ids, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets, err := scanAssets(rows)
	if err != nil {
		return nil, err
	}
	if taskID > 0 {
		if err := s.hydrateTaskAssetSources(taskID, assets); err != nil {
			return nil, err
		}
		return s.projectApprovedAssetHosts(taskID, assets)
	}
	return assets, nil
}

// ToolNodesByIDs omits evidence and long bodies. Used by authorization and
// compact member rendering; one query replaces one GetNode per list entry.
const toolCompactNodeCols = `id,kind,jsonb_strip_nulls(jsonb_build_object('summary',left(payload->>'summary',360),'text',left(payload->>'text',500),
 'confidence',left(payload->>'confidence',80),'vulnclass',left(payload->>'vulnclass',120),'severity',left(payload->>'severity',32),
 'asset_ids',payload->'asset_ids','target_ids',payload->'target_ids','target_id',payload->'target_id')),priority,state,COALESCE(origin,''),COALESCE(owner,''),COALESCE(blocked_reason,''),created_at`

func (s *ExplorationStore) ToolTerminalIntents(limit int) ([]*Node, error) {
	rows, err := s.db.Query(`SELECT `+toolCompactNodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state IN ('done','blocked','exhausted','stopped') ORDER BY id DESC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

func (s *ExplorationStore) ToolLatestTerminalSummaries(ids []int64) (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT n.id,COALESCE(a.summary,'') FROM exploration_nodes n LEFT JOIN LATERAL (
 SELECT left(COALESCE(NULLIF(summary,''),detail,''),800) summary FROM activity WHERE node_id=n.id AND exploration_id=n.exploration_id AND kind IN ('result','text')
 ORDER BY CASE WHEN kind='result' THEN 0 ELSE 1 END,id DESC LIMIT 1) a ON true
 WHERE n.exploration_id=$1 AND n.id=ANY($2::bigint[]) AND n.kind='intent' AND n.state IN ('done','blocked','exhausted','stopped')`, s.expID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var summary string
		if err := rows.Scan(&id, &summary); err != nil {
			return nil, err
		}
		out[id] = summary
	}
	return out, rows.Err()
}

func (s *ExplorationStore) ToolNodesByKind(kind string, limit int) ([]*Node, error) {
	rows, err := s.db.Query(`SELECT `+toolCompactNodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND kind=$2 ORDER BY id DESC LIMIT $3`, s.expID, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

func (s *ExplorationStore) ToolNodesByIDs(ids []int64) ([]*Node, error) {
	rows, err := s.db.Query(`SELECT `+toolCompactNodeCols+`
 FROM exploration_nodes WHERE exploration_id=$1 AND id=ANY($2::bigint[]) ORDER BY id`, s.expID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

func (s *ExplorationStore) ToolDigestMemberPage(digestID, before int64, limit int) ([]*Node, error) {
	rows, err := s.db.Query(`SELECT n.id,n.kind,jsonb_strip_nulls(jsonb_build_object('summary',left(n.payload->>'summary',360),'confidence',n.payload->'confidence')),
 n.priority,n.state,COALESCE(n.origin,''),COALESCE(n.owner,''),COALESCE(n.blocked_reason,''),n.created_at
 FROM exploration_nodes n JOIN exploration_edges e ON e.dst_id=n.id AND e.exploration_id=n.exploration_id
 WHERE e.exploration_id=$1 AND e.src_id=$2 AND e.rel='covers' AND ($3::bigint=0 OR n.id<$3) ORDER BY n.id DESC LIMIT $4`, s.expID, digestID, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// ToolNodePage applies authorization before count/cursor/LIMIT. It uses one
// statement/snapshot, independent of result count; the UI paging API is unchanged.
func (s *ExplorationStore) ToolNodePage(ctx context.Context, kind, q, severity string, assetID, before int64, limit int) (ToolNodePage, error) {
	return s.toolNodePage(ctx, kind, q, severity, assetID, before, limit, true)
}

func (s *ExplorationStore) ToolLocalNodePage(ctx context.Context, kind string, limit int) (ToolNodePage, error) {
	return s.toolNodePage(ctx, kind, "", "", 0, 0, limit, false)
}

func (s *ExplorationStore) toolNodePage(ctx context.Context, kind, q, severity string, assetID, before int64, limit int, sources bool) (ToolNodePage, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, toolNodeVisibilitySQL+`, page AS (SELECT v.*,n.payload FROM visible v JOIN exploration_nodes n ON n.id=v.id WHERE $6::bigint=0 OR v.id<$6 ORDER BY v.id DESC LIMIT $7)
 SELECT jsonb_build_object('total',(SELECT count(*) FROM visible),'nodes',COALESCE((SELECT jsonb_agg(jsonb_build_object(
 'id',id,'kind',kind,'state',state,'created_at',created_at,'source_task_id',owner,
 'inherited',exploration_id<>$1,'payload',jsonb_strip_nulls(jsonb_build_object(
 'summary',left(payload->>'summary',360),'confidence',left(payload->>'confidence',80),'vulnclass',left(payload->>'vulnclass',120),
 'name',left(payload->>'name',120),'severity',left(payload->>'severity',32),
 'intent_id',(SELECT max(e.src_id) FROM exploration_edges e JOIN exploration_nodes i ON i.id=e.src_id AND i.exploration_id=e.exploration_id
 WHERE e.dst_id=page.id AND e.exploration_id=page.exploration_id AND e.rel='yields' AND i.kind='intent'
 AND (page.exploration_id=$1 OR i.state IN ('done','blocked','exhausted','stopped'))))) ) ORDER BY id DESC) FROM page),'[]'::jsonb))`, s.expID, kind, q, severity, assetID, before, limit+1, sources).Scan(&raw)
	var out ToolNodePage
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

type ToolWorkerOutput struct {
	Activity
	DetailChars int
}

func (s *ExplorationStore) ToolWorkerTracePage(ctx context.Context, nodeID int64, q string, before int64, limit int) ([]Activity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.node_id,COALESCE(a.worker,''),COALESCE(a.kind,''),COALESCE(a.tool,''),a.is_error,left(COALESCE(a.summary,''),100)
 FROM activity a JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
 WHERE a.node_id=$1 AND a.kind NOT IN ('thinking','usage') AND n.kind='intent' AND ($3='' OR strpos(lower(COALESCE(a.summary,'')||' '||COALESCE(a.detail,'')),lower($3))>0)
 AND ($4::bigint=0 OR a.id<$4) AND (n.exploration_id=$2 OR (n.state IN ('done','blocked','exhausted','stopped') AND EXISTS(
 SELECT 1 FROM tasks current JOIN task_relations r ON r.task_id=current.id JOIN tasks source ON source.id=r.source_task_id
 WHERE current.exploration_id=$2 AND current.deleted_at IS NULL AND source.deleted_at IS NULL AND source.exploration_id=n.exploration_id)))
 ORDER BY a.id DESC LIMIT $5`, nodeID, s.expID, q, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind, &a.Tool, &a.IsError, &a.Summary); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LatestWorkerOutput reads only the requested text window from the latest output.
func (s *ExplorationStore) LatestWorkerOutput(ctx context.Context, nodeID int64, window ...int) (*ToolWorkerOutput, error) {
	offset, limit := 0, 8000
	if len(window) == 2 {
		offset, limit = window[0], window[1]
	}
	if offset < 0 || limit < 1 || limit > 24000 {
		return nil, fmt.Errorf("invalid output window")
	}
	var a ToolWorkerOutput
	err := s.db.QueryRowContext(ctx, `SELECT a.id,COALESCE(a.worker,''),a.kind,a.is_error,left(COALESCE(a.summary,''),160),
 substring(COALESCE(NULLIF(a.detail,''),a.summary,'') FROM $3+1 FOR $4),char_length(COALESCE(NULLIF(a.detail,''),a.summary,''))
 FROM activity a JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
 WHERE a.node_id=$1 AND a.kind IN ('result','text') AND n.kind='intent'
 AND (n.exploration_id=$2 OR (n.state IN ('done','blocked','exhausted','stopped') AND EXISTS (
 SELECT 1 FROM tasks current JOIN task_relations r ON r.task_id=current.id JOIN tasks source ON source.id=r.source_task_id
 WHERE current.exploration_id=$2 AND current.deleted_at IS NULL AND source.deleted_at IS NULL AND source.exploration_id=n.exploration_id)))
 ORDER BY CASE WHEN a.kind='result' THEN 0 ELSE 1 END,a.id DESC LIMIT 1`, nodeID, s.expID, offset, limit).Scan(&a.ID, &a.Worker, &a.Kind, &a.IsError, &a.Summary, &a.Detail, &a.DetailChars)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

const toolNodeVisibilitySQL = `WITH RECURSIVE current_task AS (
 SELECT id FROM tasks WHERE exploration_id=$1 AND deleted_at IS NULL
 ), contexts AS (
 SELECT $1::bigint exp,COALESCE((SELECT id FROM current_task),0) owner
 UNION SELECT t.exploration_id,t.id FROM task_relations r JOIN current_task c ON c.id=r.task_id
 JOIN tasks t ON t.id=r.source_task_id AND t.deleted_at IS NULL WHERE $8
 ), roots AS MATERIALIZED (
 SELECT n.id,n.exploration_id,n.state,n.kind,n.created_at,c.owner
 FROM exploration_nodes n JOIN contexts c ON c.exp=n.exploration_id
 WHERE n.kind=$2 AND ($3='' OR strpos(lower(COALESCE(n.payload->>'summary','')),lower($3))>0)
 AND ($4='' OR n.payload->>'severity'=$4)
 AND ($2<>'intent' OR (n.state IN ('running','done','blocked','exhausted','stopped') AND (n.exploration_id=$1 OR n.state<>'running')))
 AND ($2<>'digest' OR (n.state='active' AND EXISTS(SELECT 1 FROM exploration_edges e WHERE e.exploration_id=n.exploration_id AND e.src_id=n.id AND e.rel='covers')))
 ), lineage(root,id,exp) AS (
 SELECT id,id,exploration_id FROM roots WHERE owner<>0 OR $5::bigint<>0 UNION
 SELECT l.root,step.id,l.exp FROM lineage l CROSS JOIN LATERAL (
 SELECT CASE WHEN e.rel='covers' THEN e.dst_id ELSE e.src_id END id FROM exploration_edges e WHERE e.exploration_id=l.exp
 AND ((e.rel='covers' AND e.src_id=l.id) OR (e.rel<>'covers' AND e.dst_id=l.id))
 UNION SELECT CASE WHEN x.value ~ '^[0-9]{1,18}$' THEN x.value::bigint ELSE 0 END
 FROM exploration_nodes dn CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(dn.payload->'anchor_ids')='array' THEN dn.payload->'anchor_ids' ELSE '[]'::jsonb END)x(value)
 WHERE dn.id=l.id AND dn.exploration_id=l.exp AND dn.kind='digest'
 )step
 ), anchors AS MATERIALIZED (
 SELECT l.root,a.asset_id FROM lineage l JOIN exploration_anchors a ON a.node_id=l.id
 UNION SELECT l.root,CASE WHEN x.value ~ '^[0-9]{1,18}$' THEN x.value::bigint ELSE 0 END
 FROM lineage l JOIN exploration_nodes n ON n.id=l.id AND n.exploration_id=l.exp
 CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.payload->'asset_ids')='array' THEN n.payload->'asset_ids' ELSE '[]'::jsonb END) x(value)
 ), permission_keys AS MATERIALIZED (
 SELECT DISTINCT r.owner,a.asset_id FROM anchors a JOIN roots r ON r.id=a.root WHERE r.owner<>0
 ), permissions AS MATERIALIZED (
 SELECT owner,asset_id,task_asset_effectively_approved(owner,asset_id)
 AND task_asset_effectively_approved((SELECT id FROM current_task),asset_id) allowed FROM permission_keys
 ), visible AS MATERIALIZED (
 SELECT r.* FROM roots r WHERE ($5::bigint=0 OR EXISTS(SELECT 1 FROM anchors a WHERE a.root=r.id AND a.asset_id=$5))
 AND NOT EXISTS(SELECT 1 FROM lineage l LEFT JOIN exploration_nodes n ON n.id=l.id AND n.exploration_id=l.exp WHERE l.root=r.id AND n.id IS NULL)
 AND NOT EXISTS(SELECT 1 FROM anchors a JOIN permissions p ON p.owner=r.owner AND p.asset_id=a.asset_id WHERE a.root=r.id AND NOT p.allowed)
 )`
