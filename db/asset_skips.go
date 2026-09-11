package db

import (
	"fmt"
	"strings"
)

type TaskAssetSkip struct {
	Host     string `json:"host"`
	State    string `json:"state"`
	Attempts int64  `json:"attempts"`
}

// RememberTaskAssetDenials atomically coalesces attempts from all workers and
// the proxy. Neither successful targets nor authorization lookup errors become
// prohibitions. No asset, grant or intent is created or modified here.
func (s *AssetStore) RememberTaskAssetDenials(taskID int64, hosts []string, assetIDs []int64) ([]TaskAssetSkip, error) {
	normalized := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if host, err := NormalizeAgentHost(host, true); err == nil {
			normalized = append(normalized, host)
		}
	}
	rows, err := s.query(`WITH targets AS (
 SELECT unnest($2::text[]) host UNION SELECT task_asset_host(a) FROM assets a WHERE a.id=ANY($3::bigint[])
 AND EXISTS(SELECT 1 FROM task_asset_links l WHERE l.asset_id=a.id AND
 (l.task_id=$1 OR l.task_id IN (SELECT source_task_id FROM task_relations WHERE task_id=$1)))
 ), denied AS MATERIALIZED (
 SELECT host,task_host_approval_state($1,host) state FROM targets WHERE host<>''
 ), saved AS (
 INSERT INTO task_asset_skips(task_id,host) SELECT $1,host FROM denied WHERE state IN ('pending','revoked','blocked') ORDER BY host
 ON CONFLICT(task_id,host) DO UPDATE SET last_seen=now(),attempts=task_asset_skips.attempts+1
 RETURNING host,attempts
 ) SELECT saved.host,denied.state,saved.attempts FROM saved JOIN denied USING(host) ORDER BY host`, taskID, normalized, assetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskAssetSkip
	for rows.Next() {
		var row TaskAssetSkip
		if err := rows.Scan(&row.Host, &row.State, &row.Attempts); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *AssetStore) ActiveTaskAssetSkips(taskID int64) ([]TaskAssetSkip, error) {
	rows, err := s.query(`SELECT host,state,attempts FROM (
 SELECT host,task_host_approval_state(task_id,host) state,attempts FROM task_asset_skips WHERE task_id=$1
 ) active WHERE state IN ('pending','revoked','blocked') ORDER BY host`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskAssetSkip
	for rows.Next() {
		var row TaskAssetSkip
		if err := rows.Scan(&row.Host, &row.State, &row.Attempts); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Contains only normalized host identities, never commands, cookies or URLs.
func TaskAssetSkipMessage(rows []TaskAssetSkip) string {
	var parts []string
	for _, row := range rows {
		state := "等待审批"
		if row.State == ApprovalRevoked {
			state = "授权已撤回"
		}
		if row.State == ApprovalBlocked {
			state = "已封禁"
		}
		parts = append(parts, fmt.Sprintf("%s（%s）", row.Host, state))
	}
	if len(parts) == 0 {
		return ""
	}
	return "当前跳过清单：" + strings.Join(parts, "、") + "。跳过这些主机及其资源，不要更换工具、端口或路径重试；继续原目标的其他已授权测试，不要因此停止整个 Worker。用户批准后以最新授权为准。"
}
