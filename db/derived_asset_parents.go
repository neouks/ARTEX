package db

// ensureManualDerivedParents preserves explicit manual addition semantics for
// services selected from the global inventory. Existing parent decisions and
// parent tombstones are never changed by reattaching a child.
func (s *AssetStore) ensureManualDerivedParents(taskID int64, assetIDs []int64) error {
	rows, err := s.tx.Query(`SELECT task_asset_host(a) FROM assets a
WHERE a.id=ANY($1::bigint[]) AND a.type IN ('service','endpoint')`, assetIDs)
	if err != nil {
		return err
	}
	var hosts []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			rows.Close()
			return err
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, host := range hosts {
		if host == "" {
			continue // Retain the asset, but execution fails closed without a host.
		}
		var hasDecision bool
		if err := s.tx.QueryRow(`SELECT
EXISTS(SELECT 1 FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
       WHERE l.task_id=$1 AND a.type IN ('root_domain','subdomain','ip')
         AND task_asset_host_within($2,task_asset_host(a)))
OR EXISTS(SELECT 1 FROM task_asset_blocks b WHERE b.task_id=$1
          AND b.asset_type IN ('root_domain','subdomain','ip')
          AND task_asset_host_within($2,b.host_key))`, taskID, host).Scan(&hasDecision); err != nil {
			return err
		}
		if hasDecision {
			continue
		}
		root, _ := RootDomain(host)
		if root == "" {
			root = host
		}
		if err := s.linkHostAssets(host, root, taskID); err != nil {
			return err
		}
		if _, err := s.tx.Exec(`UPDATE task_asset_links l
SET source='manual',source_summary='手动添加服务或接口时登记父主机',
    approved_at=now(),approved_by='user',approval_reason='手动添加父主机'
FROM assets a WHERE l.task_id=$1 AND l.asset_id=a.id
  AND a.type IN ('root_domain','subdomain','ip')
  AND task_asset_host_within($2,task_asset_host(a))
  AND l.source='system' AND l.approval_state='approved'`, taskID, host); err != nil {
			return err
		}
	}
	return nil
}
