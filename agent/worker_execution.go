package agent

import "github.com/Autumn-27/artex/db"

func (t *ToolSet) validateResultAssets(ids []int64) error {
	if !t.workerExecution {
		return t.as.ValidateTaskAssetsApproved(t.taskID, ids)
	}
	if err := t.as.RememberWorkerAccess(t.taskID, t.ownerNode, nil, ids); err != nil {
		return err
	}
	return t.as.ValidateWorkerAssets(t.taskID, ids)
}

func (t *ToolSet) markResultAssetsTested(ids []int64) error {
	if t.workerExecution {
		return t.as.MarkWorkerAssetsTested(t.taskID, ids, t.worker)
	}
	return t.as.MarkTaskAssetsTested(t.taskID, ids, t.worker)
}

// Internal visibility mask only: stored/returned approval decisions stay pending.
func workerVisibility(states map[int64]string) map[int64]string {
	for id, state := range states {
		if state == db.ApprovalPending {
			states[id] = db.ApprovalApproved
		}
	}
	return states
}
