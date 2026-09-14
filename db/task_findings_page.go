package db

import (
	"context"
	"database/sql"
	"fmt"
)

// A task management view includes only its live direct sources, never recursive
// sources. Legacy graph findings remain readable but have no persistent ID.
const taskFindingsScope = `WITH scope AS (
 SELECT id, exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL
 UNION SELECT t.id,t.exploration_id FROM task_relations r JOIN tasks t ON t.id=r.source_task_id JOIN tasks owner ON owner.id=r.task_id
 WHERE r.task_id=$1 AND t.deleted_at IS NULL AND owner.deleted_at IS NULL
), visible AS (
 SELECT f.id,f.task_id,f.node_id,f.created_at FROM findings f JOIN scope s ON s.id=f.task_id
 UNION ALL
 SELECT 0::bigint,s.id,n.id,n.created_at FROM scope s JOIN exploration_nodes n ON n.exploration_id=s.exploration_id
 WHERE n.kind='finding' AND NOT EXISTS (SELECT 1 FROM findings f WHERE f.task_id=s.id AND f.node_id=n.id)
) `

func (d *DB) ListTaskFindingsPage(ctx context.Context, taskID int64, page, limit int, ascending bool) ([]*DBFinding, int, error) {
	if taskID <= 0 || page < 1 || limit < 1 || limit > 200 || page > 1000000 {
		return nil, 0, fmt.Errorf("invalid task findings pagination")
	}
	tx, err := d.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var total int
	if err = tx.QueryRowContext(ctx, taskFindingsScope+`SELECT count(*) FROM visible`, taskID).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "DESC"
	if ascending {
		order = "ASC"
	}
	// Limit identities before loading payloads/anchors. Persistent and legacy rows
	// share ordering, so neither source lists nor old nodes are sliced in Go.
	rows, err := tx.QueryContext(ctx, taskFindingsScope+`, paged AS (
 SELECT * FROM visible ORDER BY created_at `+order+`,task_id `+order+`,id `+order+`,node_id `+order+` NULLS LAST LIMIT $2 OFFSET $3
 ) SELECT p.id,p.task_id,p.node_id,COALESCE(f.vulnclass,n.payload->>'vulnclass',''),COALESCE(f.name,n.payload->>'name',''),
 COALESCE(f.severity,n.payload->>'severity','low'),COALESCE(f.summary,n.payload->>'summary',''),
 '' AS evidence,
 COALESCE(f.worker,''),COALESCE(f.asset_ids,(SELECT jsonb_agg(a.asset_id) FROM exploration_anchors a WHERE a.node_id=p.node_id),'[]'::jsonb),
 COALESCE(f.status,'pending'),p.created_at,COALESCE(t.description,''),COALESCE(f.evidence_version,0),COALESCE(f.report_evidence_version,0),
 (SELECT count(*) FROM finding_traffic_bindings b WHERE b.finding_id=p.id)
 FROM paged p LEFT JOIN findings f ON f.id=p.id LEFT JOIN exploration_nodes n ON n.id=p.node_id AND p.id=0
 JOIN tasks t ON t.id=p.task_id
 ORDER BY p.created_at `+order+`,p.task_id `+order+`,p.id `+order+`,p.node_id `+order+` NULLS LAST`, taskID, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	items, err := scanFindings(rows)
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Legacy evidence is loaded only for a selected graph record. Never promote the
// node ID into a persistent finding ID, including when an ID happens to collide.
func (d *DB) GetTaskLegacyFinding(ctx context.Context, taskID, nodeID int64) (*DBFinding, error) {
	rows, err := d.QueryContext(ctx, taskFindingsScope+`SELECT 0::bigint,v.task_id,v.node_id,
 COALESCE(n.payload->>'vulnclass',''),COALESCE(n.payload->>'name',''),COALESCE(n.payload->>'severity','low'),
 COALESCE(n.payload->>'summary',''),COALESCE(n.payload->>'evidence',''),'',
 COALESCE((SELECT jsonb_agg(asset_id) FROM exploration_anchors WHERE node_id=n.id),'[]'::jsonb),
 'pending',n.created_at,COALESCE(t.description,''),0::bigint,0::bigint,0
 FROM visible v JOIN exploration_nodes n ON n.id=v.node_id JOIN tasks t ON t.id=v.task_id WHERE v.id=0 AND v.node_id=$2`, taskID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanFindings(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items[0], nil
}
