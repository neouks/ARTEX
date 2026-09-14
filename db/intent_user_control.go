package db

import (
	"encoding/json"
	"fmt"
)

// UserCancelled is an execution lock, not a historical completion classification.
func (n *Node) UserCancelled() bool {
	if n == nil {
		return false
	}
	var p struct {
		Cancelled bool `json:"cancelled_by_user"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return p.Cancelled
}

// UserCancelledIntents is read independently of graph compaction and pagination.
func (s *ExplorationStore) UserCancelledIntents() ([]*Node, error) {
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes
	WHERE exploration_id=$1 AND kind='intent' AND payload->>'cancelled_by_user'='true' ORDER BY id`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// ReopenIntentByUser is reserved for explicit single-worker UI actions. Generic
// reopen and task recovery deliberately cannot remove the cancellation lock.
// expected prevents an outdated user request from reopening a different state.
func (s *ExplorationStore) ReopenIntentByUser(id int64, expected string) (bool, error) {
	res, err := s.db.Exec(`UPDATE exploration_nodes SET state='open', completed_at=NULL,
	blocked_reason=NULL, content_version=content_version+1,
	payload=CASE WHEN payload->>'cancelled_by_user'='true'
	THEN payload || jsonb_build_object('cancelled_by_user',false,'reopened_by_user_at',now()) ELSE payload END
	WHERE id=$1 AND exploration_id=$2 AND kind='intent' AND state=$3
	AND state IN ('paused','blocked','exhausted','stopped')`, id, s.expID, expected)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RestoreUserReopen runs while the server's worker-admission lock is held.
func (s *ExplorationStore) RestoreUserReopen(before *Node) error {
	res, err := s.db.Exec(`UPDATE exploration_nodes SET state=$3, payload=$4,
	blocked_reason=NULLIF($5,''), content_version=content_version+1,
	completed_at=CASE WHEN $3 IN ('blocked','exhausted','stopped') THEN now() ELSE NULL END
	WHERE id=$1 AND exploration_id=$2 AND kind='intent' AND state='open'`,
		before.ID, s.expID, before.State, string(before.Payload), before.BlockedReason)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return fmt.Errorf("%w: cannot restore user reopen", ErrIntentStateConflict)
	}
	return err
}
