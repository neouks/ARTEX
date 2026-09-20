package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

const ExecutionManaged = "managed"
const ExecutionManual = "manual"

func (s *ExplorationStore) ExecutionMode() (string, error) {
	var mode string
	err := s.db.QueryRow(`SELECT execution_mode FROM tasks WHERE exploration_id=$1 AND deleted_at IS NULL`, s.expID).Scan(&mode)
	// Standalone exploration stores retain their existing automatic behavior.
	if err == sql.ErrNoRows {
		return ExecutionManaged, nil
	}
	return mode, err
}

func (s *ExplorationStore) SetExecutionMode(mode string) (bool, error) {
	if mode != ExecutionManaged && mode != ExecutionManual {
		return false, fmt.Errorf("execution_mode must be managed|manual")
	}
	tx, err := s.beginQueue()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var before string
	if err = tx.QueryRow(`SELECT execution_mode FROM tasks WHERE exploration_id=$1 AND deleted_at IS NULL FOR UPDATE`, s.expID).Scan(&before); err != nil {
		return false, err
	}
	if before == mode {
		return false, tx.Commit()
	}
	if mode == ExecutionManual {
		if _, err = tx.Exec(`UPDATE exploration_nodes SET payload=payload || jsonb_build_object('dispatch_requested',true) WHERE exploration_id=$1 AND kind='intent' AND state='running'`, s.expID); err != nil {
			return false, err
		}
		if _, err = tx.Exec(`UPDATE exploration_nodes SET payload=payload-'dispatch_requested' WHERE exploration_id=$1 AND kind='intent' AND state='open'`, s.expID); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(`UPDATE tasks SET execution_mode=$2 WHERE exploration_id=$1 AND deleted_at IS NULL`, s.expID, mode); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE explorations SET worker_queue_version=worker_queue_version+1 WHERE id=$1`, s.expID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (n *Node) DispatchRequested() bool {
	var p struct {
		Requested bool `json:"dispatch_requested"`
	}
	if n != nil {
		_ = json.Unmarshal(n.Payload, &p)
	}
	return p.Requested
}

// SetIntentDispatch shares the queue lock with claims and mode transitions.
// It changes only admission metadata, never lifecycle state or asset approval.
func (s *ExplorationStore) SetIntentDispatch(id int64, requested bool) error {
	res, err := s.queueExec(`UPDATE exploration_nodes SET payload=payload || jsonb_build_object('dispatch_requested',$3::boolean)
 WHERE id=$1 AND exploration_id=$2 AND kind='intent' AND state='open'
 AND payload->>'cancelled_by_user' IS DISTINCT FROM 'true'
 AND (NOT $3 OR NOT EXISTS (SELECT 1 FROM exploration_anchors a JOIN tasks t ON t.exploration_id=exploration_nodes.exploration_id AND t.deleted_at IS NULL WHERE a.node_id=exploration_nodes.id AND NOT task_asset_effectively_approved(t.id,a.asset_id)))`, id, s.expID, requested)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err == nil && count != 1 {
		return ErrIntentStateConflict
	}
	return err
}

// Included in every open->running claim, independently of model/tool behavior.
const intentDispatchPredicate = ` AND NOT EXISTS (SELECT 1 FROM tasks ended_task WHERE ended_task.exploration_id=exploration_nodes.exploration_id AND (ended_task.deleted_at IS NOT NULL OR ended_task.status IN ('done','failed','timeout'))) AND (payload->>'dispatch_requested'='true' OR NOT EXISTS
 (SELECT 1 FROM tasks dispatch_task WHERE dispatch_task.exploration_id=exploration_nodes.exploration_id AND dispatch_task.deleted_at IS NULL AND dispatch_task.execution_mode='manual'))`

func (s *ExplorationStore) FinishByUser() error {
	_, err := s.queueExec(`UPDATE tasks SET status='done',paused=false,queued=false,queued_at=NULL,queue_mode='',completed_at=now() WHERE exploration_id=$1 AND deleted_at IS NULL`, s.expID)
	return err
}
