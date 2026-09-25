package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// All graph mutations acquire the exploration advisory lock before row locks.
// This is also the existing intent-deduplication lock; it works across processes.
func (s *ExplorationStore) beginQueue() (*sql.Tx, error) {
	return s.beginQueueContext(context.Background())
}

func (s *ExplorationStore) beginQueueContext(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, -s.expID); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func (s *ExplorationStore) queueExec(query string, args ...any) (sql.Result, error) {
	tx, err := s.beginQueue()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(query, args...)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

type WorkerQueue struct {
	ExecutionMode string  `json:"execution_mode"`
	Version       int64   `json:"version"`
	Manual        bool    `json:"manual"`
	Items         []*Node `json:"items"`
}

func (s *ExplorationStore) queueSnapshot(tx *sql.Tx) (WorkerQueue, error) {
	q := WorkerQueue{Items: []*Node{}}
	if err := tx.QueryRow(`SELECT worker_queue_manual,worker_queue_version FROM explorations WHERE id=$1`, s.expID).Scan(&q.Manual, &q.Version); err != nil {
		return q, err
	}
	if err := tx.QueryRow(`SELECT COALESCE((SELECT execution_mode FROM tasks WHERE exploration_id=$1 AND deleted_at IS NULL),'managed')`, s.expID).Scan(&q.ExecutionMode); err != nil {
		return q, err
	}
	rows, err := tx.Query(`SELECT `+nodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state='open'
 AND payload->>'cancelled_by_user' IS DISTINCT FROM 'true'
 ORDER BY CASE WHEN $2 THEN queue_position ELSE 0 END, CASE WHEN $2 THEN 0 ELSE priority END DESC,id`, s.expID, q.Manual)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	nodes, err := scanNodes(rows)
	if nodes != nil {
		q.Items = nodes
	}
	return q, err
}
func (s *ExplorationStore) WorkerQueue() (WorkerQueue, error) {
	tx, err := s.beginQueue()
	if err != nil {
		return WorkerQueue{}, err
	}
	defer tx.Rollback()
	q, err := s.queueSnapshot(tx)
	if err != nil {
		return q, err
	}
	return q, tx.Commit()
}
func (s *ExplorationStore) MoveWorker(id int64, before *int64, version int64) (WorkerQueue, error) {
	tx, err := s.beginQueue()
	if err != nil {
		return WorkerQueue{}, err
	}
	defer tx.Rollback()
	q, err := s.queueSnapshot(tx)
	if err != nil {
		return q, err
	}
	if q.Version != version {
		return q, ErrIntentStateConflict
	}
	var moving *Node
	remaining := make([]*Node, 0, len(q.Items))
	validTarget := before == nil
	for _, n := range q.Items {
		if n.ID == id {
			moving = n
		} else {
			remaining = append(remaining, n)
		}
		if before != nil && n.ID == *before {
			validTarget = true
		}
	}
	if moving == nil || !validTarget || (before != nil && *before == id) {
		return q, ErrIntentStateConflict
	}
	ordered := make([]*Node, 0, len(q.Items))
	for _, n := range remaining {
		if before != nil && n.ID == *before {
			ordered = append(ordered, moving)
		}
		ordered = append(ordered, n)
	}
	if before == nil {
		ordered = append(ordered, moving)
	}
	if _, err = tx.Exec(`UPDATE explorations SET worker_queue_manual=true,worker_queue_version=worker_queue_version+1 WHERE id=$1`, s.expID); err != nil {
		return q, err
	}
	for i, n := range ordered {
		if _, err = tx.Exec(`UPDATE exploration_nodes SET queue_position=$1 WHERE id=$2 AND exploration_id=$3`, i+1, n.ID, s.expID); err != nil {
			return q, err
		}
	}
	q, err = s.queueSnapshot(tx)
	if err != nil {
		return q, err
	}
	return q, tx.Commit()
}

// Query and claim share one transaction so a reorder cannot invalidate a frontier snapshot.
func (s *ExplorationStore) ClaimNextWorker(owner string, eligible func(*Node) bool) (*Node, error) {
	tx, err := s.beginQueue()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q, err := s.queueSnapshot(tx)
	if err != nil {
		return nil, err
	}
	for _, n := range q.Items {
		if q.ExecutionMode == ExecutionManual && !n.DispatchRequested() {
			continue
		}
		if eligible != nil && !eligible(n) {
			continue
		}
		res, err := tx.Exec(`UPDATE exploration_nodes SET state='running',owner=$1,payload=payload || jsonb_build_object('dispatch_requested',true) WHERE id=$2 AND exploration_id=$3 AND state='open' AND payload->>'cancelled_by_user' IS DISTINCT FROM 'true'
 AND NOT EXISTS (SELECT 1 FROM exploration_anchors ea JOIN tasks t ON t.exploration_id=exploration_nodes.exploration_id AND t.deleted_at IS NULL WHERE ea.node_id=exploration_nodes.id AND NOT `+intentAssetAllowedSQL("t.id", "ea.asset_id", "exploration_nodes")+`)`+intentDispatchPredicate, owner, n.ID, s.expID)
		if err != nil {
			return nil, err
		}
		count, _ := res.RowsAffected()
		if count == 1 {
			return n, tx.Commit()
		}
	}
	return nil, tx.Commit()
}

// DeleteWorker retains products and detached metering, never calls CancelIntent's destructive legacy cleanup.
func (s *ExplorationStore) DeleteWorker(id int64) (bool, error) {
	return s.DeleteWorkerWithCleanup(id, nil)
}

// beforeDelete stages external files while the queue lock still excludes claims.
func (s *ExplorationStore) DeleteWorkerWithCleanup(id int64, beforeDelete func() error) (bool, error) {
	tx, err := s.beginQueue()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM deleted_workers WHERE exploration_id=$1 AND intent_id=$2)`, s.expID, id).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		if beforeDelete != nil {
			if err = beforeDelete(); err != nil {
				return false, err
			}
		}
		return false, tx.Commit()
	}
	var raw []byte
	var state string
	err = tx.QueryRow(`SELECT payload,state FROM exploration_nodes WHERE exploration_id=$1 AND id=$2 AND kind='intent' FOR UPDATE`, s.expID, id).Scan(&raw, &state)
	if err != nil {
		return false, fmt.Errorf("%w: Worker 不存在或为继承只读项", ErrIntentStateConflict)
	}
	var payload map[string]any
	if err = json.Unmarshal(raw, &payload); err != nil {
		return false, err
	}
	if state != "open" && !(state == "stopped" && payload["cancelled_by_user"] == true) {
		return false, fmt.Errorf("%w: 仅等待运行或用户已取消的 Worker 可以删除", ErrIntentStateConflict)
	}
	// Persist the original source on surviving products before their graph edges cascade.
	source, _ := json.Marshal(map[string]any{"id": id, "summary": payload["summary"], "deleted_by_user": true})
	if _, err = tx.Exec(`UPDATE exploration_nodes SET payload=payload||jsonb_build_object('deleted_worker_source',$3::jsonb),content_version=content_version+1
 WHERE exploration_id=$1 AND id IN(SELECT dst_id FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2)`, s.expID, id, string(source)); err != nil {
		return false, err
	}
	buckets, err := intentTokenRollup(tx, s.expID, id)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(`INSERT INTO deleted_workers(exploration_id,intent_id,payload) VALUES($1,$2,$3)`, s.expID, id, string(raw)); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`DELETE FROM activity WHERE exploration_id=$1 AND node_id=$2`, s.expID, id); err != nil {
		return false, err
	}
	for _, b := range buckets {
		metadata, _ := json.Marshal(map[string]any{"deleted_intent_id": id, "token_day": b.Day.Format(time.DateOnly), "token_rollup": true})
		if _, err = tx.Exec(`INSERT INTO activity(exploration_id,worker,kind,summary,metadata,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,created_at)
  VALUES($1,'token-ledger','result',$2,$3,$4,$5,$6,$7,$8)`, s.expID, fmt.Sprintf("已删除 Worker #%d 的 Token 计量", id), string(metadata), b.Usage.InputTokens, b.Usage.OutputTokens, b.Usage.CacheReadTokens, b.Usage.CacheWriteTokens, b.Day); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO activity(exploration_id,worker,kind,summary,metadata) VALUES($1,'system','result',$2,$3)`, s.expID, fmt.Sprintf("用户已删除 Worker #%d：%v；原 Worker 不可再次调度", id, payload["summary"]), string(source)); err != nil {
		return false, err
	}
	if beforeDelete != nil {
		if err = beforeDelete(); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(`DELETE FROM exploration_nodes WHERE exploration_id=$1 AND id=$2`, s.expID, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
func (s *ExplorationStore) DeletedWorkers() ([]*Node, error) {
	rows, err := s.db.Query(`SELECT intent_id,payload FROM deleted_workers WHERE exploration_id=$1 ORDER BY intent_id`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n := &Node{}
		if err = rows.Scan(&n.ID, &n.Payload); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
