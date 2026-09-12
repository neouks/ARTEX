package db

import (
	"fmt"
	"strings"
)

// WithWorkerRead changes only the model read projection, not stored approval.
func (s *AssetStore) WithWorkerRead() *AssetStore {
	copy := *s
	copy.workerRead = true
	return &copy
}

func workerReadSQL(query string, worker bool) string {
	if worker {
		return strings.ReplaceAll(query, "task_asset_effectively_approved(", "task_asset_worker_executable(")
	}
	return query
}

func (s *AssetStore) ValidateWorkerAssets(taskID int64, ids []int64) error {
	states, err := s.TaskAssetApprovalStates(taskID, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if states[id] != ApprovalApproved && states[id] != ApprovalPending {
			return fmt.Errorf("%w: asset %d 不属于本任务或已被主动限制 (%s)", ErrTaskAssetNotApproved, id, states[id])
		}
	}
	return nil
}

func (s *AssetStore) ValidateWorkerHosts(taskID int64, hosts []string) error {
	normalized := make([]string, 0, len(hosts))
	for _, host := range hosts {
		h, err := NormalizeAgentHost(host, true)
		if err != nil {
			return fmt.Errorf("%w: host %q", ErrTaskAssetInvalid, host)
		}
		normalized = append(normalized, h)
	}
	states, err := s.TaskHostApprovalStates(taskID, normalized)
	if err != nil {
		return err
	}
	for _, host := range normalized {
		if states[host] != ApprovalApproved && states[host] != ApprovalPending {
			return fmt.Errorf("%w: host %s 已被主动限制或任务无效 (%s)", ErrTaskAssetNotApproved, host, states[host])
		}
	}
	return nil
}

// RememberWorkerAccess tracks execution separately from planning anchors. Save
// before checking restrictions, so a concurrent revoke is caught by either the
// subsequent check or the cancellation lookup. No task/asset is authorized here.
func (s *AssetStore) RememberWorkerAccess(taskID, intentID int64, hosts []string, ids []int64) error {
	if len(hosts) == 0 && len(ids) == 0 {
		return nil
	}
	canonical := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host, err := NormalizeAgentHost(host, true)
		if err != nil {
			return ErrTaskAssetInvalid
		}
		canonical = append(canonical, host)
	}
	var active bool
	err := s.db.QueryRow(`WITH active AS MATERIALIZED (
 SELECT 1 FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id
 WHERE t.id=$1 AND t.deleted_at IS NULL AND n.id=$2 AND n.kind='intent' AND n.state='running'
 ), saved AS (INSERT INTO task_worker_asset_access(task_id,intent_id,host,asset_id)
 SELECT $1,$2,target.host,target.asset_id FROM (
 SELECT unnest($3::text[]) host,0::bigint asset_id
 UNION SELECT task_asset_host(a),a.id FROM assets a WHERE a.id=ANY($4::bigint[])
	) target WHERE EXISTS(SELECT 1 FROM active)
 ON CONFLICT DO NOTHING RETURNING 1) SELECT EXISTS(SELECT 1 FROM active)`, taskID, intentID, canonical, ids).Scan(&active)
	if err == nil && !active {
		return fmt.Errorf("%w: Worker 不属于本任务或已停止", ErrTaskAssetInvalid)
	}
	return err
}

func (s *AssetStore) MarkWorkerAssetsTested(taskID int64, ids []int64, worker string) error {
	_, err := s.db.Exec(`UPDATE task_asset_links SET tested=true,tested_at=COALESCE(tested_at,now()),tested_by=$3
 WHERE task_id=$1 AND asset_id=ANY($2::bigint[]) AND task_asset_worker_executable(task_id,asset_id)`, taskID, ids, worker)
	return err
}
