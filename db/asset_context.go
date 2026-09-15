package db

import "encoding/json"

type NodeAccess struct {
	CanRead bool     `json:"can_read"`
	Reasons []string `json:"reasons,omitempty"`
}

// TaskNodeAccess revalidates historic graph results in one query. Covers
// edges traverse into digest members; ordinary edges traverse ancestor lineage.
func (s *AssetStore) TaskNodeAccess(taskID int64, ids []int64) (map[int64]NodeAccess, error) {
	states := make(map[int64]NodeAccess, len(ids))
	for _, id := range ids {
		states[id] = NodeAccess{Reasons: []string{"unavailable"}}
	}
	if len(ids) == 0 {
		return states, nil
	}
	rows, err := s.query(`WITH RECURSIVE context_exps AS (
 SELECT exploration_id,id owner FROM tasks WHERE id=$1 AND deleted_at IS NULL
 UNION SELECT t.exploration_id,t.id FROM task_relations r JOIN tasks t ON t.id=r.source_task_id WHERE r.task_id=$1 AND t.deleted_at IS NULL AND EXISTS(SELECT 1 FROM tasks current_task WHERE current_task.id=$1 AND current_task.deleted_at IS NULL)
), roots AS (
 SELECT n.id,n.exploration_id,c.owner FROM exploration_nodes n JOIN context_exps c ON c.exploration_id=n.exploration_id WHERE n.id=ANY($2::bigint[])
), lineage(root_id,id,exp_id) AS (
 SELECT id,id,exploration_id FROM roots
 UNION
 SELECT l.root_id,step.id,l.exp_id FROM lineage l CROSS JOIN LATERAL (
 SELECT CASE WHEN e.rel='covers' THEN e.dst_id ELSE e.src_id END id FROM exploration_edges e WHERE e.exploration_id=l.exp_id
 AND ((e.rel='covers' AND e.src_id=l.id) OR (e.rel<>'covers' AND e.dst_id=l.id))
 UNION SELECT CASE WHEN x.value ~ '^[0-9]{1,18}$' THEN x.value::bigint ELSE 0 END
 FROM exploration_nodes dn CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(dn.payload->'anchor_ids')='array' THEN dn.payload->'anchor_ids' ELSE '[]'::jsonb END)x(value)
 WHERE dn.id=l.id AND dn.exploration_id=l.exp_id AND dn.kind='digest'
 )step
), anchors AS MATERIALIZED (
 SELECT l.root_id,a.asset_id FROM lineage l JOIN exploration_anchors a ON a.node_id=l.id
 UNION SELECT l.root_id,CASE WHEN x.value ~ '^[0-9]{1,18}$' THEN x.value::bigint ELSE 0 END
 FROM lineage l JOIN exploration_nodes n ON n.id=l.id
 CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.payload->'asset_ids')='array' THEN n.payload->'asset_ids' ELSE '[]'::jsonb END)x(value)
), permission_keys AS MATERIALIZED (
 SELECT DISTINCT r.owner,a.asset_id FROM roots r JOIN anchors a ON a.root_id=r.id
), permissions AS MATERIALIZED (
 SELECT owner,asset_id,task_asset_effectively_approved($1,asset_id) AND task_asset_effectively_approved(owner,asset_id) allowed, task_asset_effective_approval_state($1,asset_id) current_state, task_asset_effective_approval_state(owner,asset_id) owner_state FROM permission_keys
)
SELECT r.id,COALESCE((SELECT jsonb_agg(DISTINCT reason ORDER BY reason) FROM (
 SELECT 'unavailable' reason WHERE EXISTS(SELECT 1 FROM lineage l LEFT JOIN exploration_nodes n ON n.id=l.id AND n.exploration_id=l.exp_id WHERE l.root_id=r.id AND (n.id IS NULL OR (NOT $3::boolean AND n.kind='digest' AND NOT EXISTS(SELECT 1 FROM exploration_edges e WHERE e.exploration_id=n.exploration_id AND e.src_id=n.id AND e.rel='covers'))))
 UNION ALL
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM assets asset WHERE asset.id=a.asset_id) THEN 'unavailable' ELSE st.reason END FROM anchors a JOIN permissions p ON p.owner=r.owner AND p.asset_id=a.asset_id
 CROSS JOIN LATERAL (VALUES(p.current_state),(p.owner_state)) st(reason)
 WHERE a.root_id=r.id AND NOT p.allowed AND st.reason<>'approved'
 ) denied), '[]'::jsonb)
FROM roots r`, taskID, ids, s.workerRead)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var reasons []string
		if err := json.Unmarshal(raw, &reasons); err != nil {
			return nil, err
		}
		states[id] = NodeAccess{CanRead: len(reasons) == 0, Reasons: reasons}
	}
	return states, rows.Err()
}

// Keep the legacy visibility projection for callers that only need a gate.
func (s *AssetStore) TaskNodeApprovalStates(taskID int64, ids []int64) (map[int64]string, error) {
	access, err := s.TaskNodeAccess(taskID, ids)
	if err != nil {
		return nil, err
	}
	states := make(map[int64]string, len(access))
	for id, row := range access {
		if row.CanRead {
			states[id] = ApprovalApproved
		} else {
			states[id] = row.Reasons[0]
		}
	}
	return states, nil
}

// TaskTargetAssetStates exposes only identities belonging to this task or a
// live direct source. Unknown and unrelated identities are indistinguishable.
func (s *AssetStore) TaskTargetAssetStates(taskID int64, ids []int64) (map[int64]string, error) {
	states := make(map[int64]string, len(ids))
	for _, id := range ids {
		states[id] = "unavailable"
	}
	if len(ids) == 0 {
		return states, nil
	}
	rows, err := s.query(`SELECT a.id,task_asset_effective_approval_state($1,a.id) FROM assets a
 WHERE a.id=ANY($2::bigint[]) AND EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL) AND EXISTS (
 SELECT 1 FROM task_asset_links l JOIN tasks t ON t.id=l.task_id AND t.deleted_at IS NULL
 WHERE l.asset_id=a.id AND (l.task_id=$1 OR l.task_id IN (SELECT source_task_id FROM task_relations WHERE task_id=$1)))`, taskID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		states[id] = state
	}
	return states, rows.Err()
}
