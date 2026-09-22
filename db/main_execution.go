package db

import (
	"encoding/json"
	"fmt"
)

// A trusted main-agent dispatch grants this intent a pending-only exception.
// It never changes asset approval and is separate from the manual queue choice.
func (n *Node) AllowsPendingAssets() bool {
	if n == nil {
		return false
	}
	var p struct {
		Permission struct {
			Issuer string `json:"issuer"`
		} `json:"pending_asset_execution"`
	}
	return json.Unmarshal(n.Payload, &p) == nil && p.Permission.Issuer == "mainagent"
}
func (s *AssetStore) ValidateIntentAssets(taskID int64, n *Node, ids []int64) error {
	if n.AllowsPendingAssets() {
		return s.ValidateWorkerAssets(taskID, ids)
	}
	return s.ValidateTaskAssetsApproved(taskID, ids)
}
func (s *AssetStore) ValidateIntentHosts(taskID int64, n *Node, hosts []string) error {
	if n.AllowsPendingAssets() {
		return s.ValidateWorkerHosts(taskID, hosts)
	}
	return s.ValidateTaskHostsApproved(taskID, hosts)
}
func intentAssetAllowedSQL(task, asset, node string) string {
	return fmt.Sprintf("(CASE WHEN %s.payload->'pending_asset_execution'->>'issuer'='mainagent' THEN task_asset_worker_executable(%s,%s) ELSE task_asset_effectively_approved(%s,%s) END)", node, task, asset, task, asset)
}

// GrantMainIntentDispatch runs only after host task admission succeeds. The
// same queue lock used by claims makes selection and permission atomic.
func (s *ExplorationStore) GrantMainIntentDispatch(id int64) error {
	result, err := s.queueExec(`UPDATE exploration_nodes SET payload=payload || jsonb_build_object('dispatch_requested',true,'pending_asset_execution',COALESCE(payload->'pending_asset_execution',jsonb_build_object('issuer','mainagent','granted_at',now())))
 WHERE id=$1 AND exploration_id=$2 AND kind='intent' AND state='open' AND payload->>'cancelled_by_user' IS DISTINCT FROM 'true'
 AND NOT EXISTS(SELECT 1 FROM exploration_anchors a JOIN tasks t ON t.exploration_id=exploration_nodes.exploration_id WHERE a.node_id=exploration_nodes.id AND NOT task_asset_worker_executable(t.id,a.asset_id))`, id, s.expID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrIntentStateConflict
	}
	return err
}

// Main execution uses the same read projection as Workers; raw approval state
// stays untouched and is still returned by approval metadata tools.
func (s *AssetStore) WithExecutionRead() *AssetStore { return s.WithWorkerRead() }
func (s *AssetStore) ExecutionRead() bool            { return s.workerRead }
