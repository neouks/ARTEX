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
func (s *AssetStore) RememberTaskAssetDenials(taskID int64, hosts []string, assetIDs []int64, scopes ...string) ([]TaskAssetSkip, error) {
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
 INSERT INTO task_asset_skips(task_id,host,observer_scopes) SELECT $1,host,COALESCE($4::text[],'{}') FROM denied WHERE state IN ('pending','revoked','blocked') ORDER BY host
 ON CONFLICT(task_id,host) DO UPDATE SET last_seen=now(),attempts=task_asset_skips.attempts+1,
 observer_scopes=ARRAY(SELECT DISTINCT scope FROM unnest(task_asset_skips.observer_scopes || EXCLUDED.observer_scopes) scope ORDER BY scope)
 RETURNING host,attempts
 ) SELECT saved.host,denied.state,saved.attempts FROM saved JOIN denied USING(host) ORDER BY host`, taskID, normalized, assetIDs, scopes)
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

func (s *AssetStore) ActiveTaskAssetSkips(taskID int64, scopes ...string) ([]TaskAssetSkip, error) {
	scope := ""
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	rows, err := s.query(`SELECT host,state,attempts FROM (
 SELECT host,task_host_approval_state(task_id,host) state,attempts FROM task_asset_skips WHERE task_id=$1
 AND ($2='' OR $2=ANY(observer_scopes) OR ($2='planner' AND EXISTS(
 SELECT 1 FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id
 WHERE t.id=$1 AND n.kind='intent' AND n.state IN ('open','running','paused')
 AND ('worker:'||n.id::text)=ANY(observer_scopes))))
 ) active WHERE state IN ('pending','revoked','blocked') ORDER BY host`, taskID, scope)
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
	list := TaskAssetSkipList(rows)
	if list == "" {
		return ""
	}
	return list + "。" + TaskAssetSkipRule
}

const TaskAssetSkipRule = "跳过清单中的主机及资源，不换工具、端口或路径重试；继续其他已授权测试，不因此停止 Worker。历史拦截不是永久禁令，以当前授权为准。"

func TaskAssetSkipList(rows []TaskAssetSkip) string {
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
	return "跳过：" + strings.Join(parts, "、")
}
