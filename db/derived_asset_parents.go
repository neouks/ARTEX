package db

// Register only exact parents of user-selected derived assets. Existing explicit
// parent revocations and tombstones remain effective.
func (s *AssetStore) ensureManualDerivedParents(taskID int64, assetIDs []int64) error {
	if _, err := s.tx.Exec(`SELECT set_config('artex.user_asset_registration','on',true)`); err != nil {
		return err
	}
	for _, id := range assetIDs {
		if err := authorizeUserAsset(s, taskID, id, "manual", "用户提供：手动关联", false); err != nil {
			return err
		}
	}
	return seedUserAssetGrants(s.tx, taskID)
}
