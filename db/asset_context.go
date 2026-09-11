package db

// TaskNodeApprovalStates revalidates historic graph results in one query. Covers
// edges traverse into digest members; ordinary edges traverse ancestor lineage.
func (s *AssetStore) TaskNodeApprovalStates(taskID int64, ids []int64) (map[int64]string, error) {
	states := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return states, nil
	}
	rows, err := s.query(`WITH RECURSIVE context_exps AS (
 SELECT exploration_id FROM tasks WHERE id=$1
 UNION SELECT t.exploration_id FROM task_relations r JOIN tasks t ON t.id=r.source_task_id WHERE r.task_id=$1
), roots AS (
 SELECT id,exploration_id FROM exploration_nodes WHERE id=ANY($2::bigint[])
 AND exploration_id IN (SELECT exploration_id FROM context_exps)
), lineage(root_id,id,exp_id) AS (
 SELECT id,id,exploration_id FROM roots
 UNION
 SELECT l.root_id,CASE WHEN e.rel='covers' THEN e.dst_id ELSE e.src_id END,l.exp_id
 FROM lineage l JOIN exploration_edges e ON e.exploration_id=l.exp_id
 AND ((e.rel='covers' AND e.src_id=l.id) OR (e.rel<>'covers' AND e.dst_id=l.id))
)
SELECT r.id,CASE WHEN EXISTS(SELECT 1 FROM lineage l JOIN exploration_anchors a ON a.node_id=l.id
 WHERE l.root_id=r.id AND NOT task_asset_effectively_approved($1,a.asset_id)) THEN 'pending' ELSE 'approved' END
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
