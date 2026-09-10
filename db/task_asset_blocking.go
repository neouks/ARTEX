package db

import (
	"database/sql"
	"fmt"
	"strings"
)

func rejectDeletedApprovalAssets(tx *sql.Tx, taskID int64, ids []int64) error {
	var deleted bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM assets a JOIN task_asset_blocks b
ON b.task_id=$1 AND (b.asset_id=a.id OR b.asset_key=task_asset_identity_key(a))
WHERE a.id=ANY($2::bigint[]) AND b.block_kind='deleted')`, taskID, ids).Scan(&deleted)
	if err != nil {
		return err
	}
	if deleted {
		return fmt.Errorf("%w: 删除封禁资产请先重新关联", ErrTaskAssetBlocked)
	}
	return nil
}

// BlockTaskAssets preserves associations and test history while durably denying
// execution. Validate the entire selection before changing any record.
func (s *AssetStore) BlockTaskAssets(taskID int64, assetIDs []int64, actor, reason string) error {
	ids, err := normalizeTaskAssetIDs(assetIDs)
	if err != nil {
		return err
	}
	actor, reason = strings.TrimSpace(actor), strings.TrimSpace(reason)
	if actor == "" {
		actor = "user"
	}
	if reason == "" {
		reason = "用户封禁测试授权"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := rejectDerivedApprovalAssets(tx, ids); err != nil {
		return err
	}
	assets, err := lockTaskAssetApprovalRows(tx, taskID, ids)
	if err != nil {
		return err
	}
	if err := rejectDeletedApprovalAssets(tx, taskID, ids); err != nil {
		return err
	}
	for _, id := range ids {
		asset := assets[id]
		key, host := AssetKey(asset)
		if _, err := tx.Exec(`INSERT INTO task_asset_blocks(task_id,asset_key,asset_type,host_key,asset_id,reason,blocked_by,block_kind)
VALUES($1,$2,$3,$4,$5,$6,$7,'manual') ON CONFLICT(task_id,asset_key) DO NOTHING`, taskID, key, asset.Type, host, id, reason, actor); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE task_asset_links SET approval_state='blocked'
WHERE task_id=$1 AND asset_id=ANY($2::bigint[]) AND approval_state<>'blocked'`, taskID, ids); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AssetStore) directTaskAssetBlockKind(taskID, assetID int64) (string, error) {
	var kind string
	err := s.db.QueryRow(`SELECT b.block_kind FROM task_asset_blocks b JOIN assets a
ON b.asset_id=a.id OR b.asset_key=task_asset_identity_key(a)
WHERE b.task_id=$1 AND a.id=$2 LIMIT 1`, taskID, assetID).Scan(&kind)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return kind, err
}
