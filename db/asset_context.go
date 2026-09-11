package db

// TaskNodeApprovalStates revalidates historic graph results in one query. Covers
// edges traverse into digest members; ordinary edges traverse ancestor lineage.
func (s *AssetStore) TaskNodeApprovalStates(taskID int64, ids []int64) (map[int64]string, error) {
	states := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return states, nil
	}
	rows, err := s.query(`WITH RECURSIVE context_exps AS (
 SELECT exploration_id,id owner FROM tasks WHERE id=$1 AND deleted_at IS NULL
 UNION SELECT t.exploration_id,t.id FROM task_relations r JOIN tasks t ON t.id=r.source_task_id WHERE r.task_id=$1 AND t.deleted_at IS NULL
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
 SELECT owner,asset_id,task_asset_effectively_approved($1,asset_id) AND task_asset_effectively_approved(owner,asset_id) allowed FROM permission_keys
)
SELECT r.id,CASE WHEN EXISTS(SELECT 1 FROM lineage l LEFT JOIN exploration_nodes n ON n.id=l.id AND n.exploration_id=l.exp_id WHERE l.root_id=r.id AND n.id IS NULL)
 OR EXISTS(SELECT 1 FROM anchors a JOIN permissions p ON p.owner=r.owner AND p.asset_id=a.asset_id
 WHERE a.root_id=r.id AND NOT p.allowed) THEN 'pending' ELSE 'approved' END
FROM roots r`, taskID, ids)
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
