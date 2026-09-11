package db

import (
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type TaskAssetApprovalGroup struct {
	TaskAssetApproval
	GroupKey    string   `json:"group_key"`
	AssetIDs    []int64  `json:"asset_ids"`
	RecordTypes []string `json:"record_types"`
	Sources     []string `json:"sources"`
	MixedState  bool     `json:"mixed_state"`
}

func approvalGroupKey(v TaskAssetApproval) string {
	key := "id:" + strconv.FormatInt(v.AssetID, 10)
	if v.AssetType == "root_domain" || v.AssetType == "subdomain" || v.AssetType == "ip" {
		key = "host:" + DomainKey(v.Name)
	}
	return strconv.FormatInt(v.SourceTaskID, 10) + "|" + key
}
func approvalRank(v TaskAssetApproval) int {
	if v.Blocked || v.ApprovalState == ApprovalBlocked {
		return 3
	}
	if v.ApprovalState == ApprovalRevoked {
		return 2
	}
	if v.ApprovalState == ApprovalPending {
		return 1
	}
	return 0
}
func (s *AssetStore) ListTaskAssetApprovalGroups(taskID int64) ([]TaskAssetApprovalGroup, error) {
	rows, err := s.ListTaskAssetApprovals(taskID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, v := range rows {
		if v.AssetID > 0 {
			ids = append(ids, v.AssetID)
		}
	}
	assets, err := s.GetByIDs(ids)
	if err != nil {
		return nil, err
	}
	types := map[int64]string{}
	for _, a := range assets {
		types[a.ID] = a.RecordType
	}
	groups := []TaskAssetApprovalGroup{}
	indices := map[string]int{}
	for _, v := range rows {
		key := approvalGroupKey(v)
		i, ok := indices[key]
		if !ok {
			i = len(groups)
			indices[key] = i
			groups = append(groups, TaskAssetApprovalGroup{TaskAssetApproval: v, GroupKey: key})
		}
		g := &groups[i]
		if approvalRank(g.TaskAssetApproval) != approvalRank(v) {
			g.MixedState = true
		}
		if approvalRank(v) > approvalRank(g.TaskAssetApproval) {
			g.TaskAssetApproval = v
		}
		if v.AssetID > 0 && !slices.Contains(g.AssetIDs, v.AssetID) {
			g.AssetIDs = append(g.AssetIDs, v.AssetID)
		}
		if rt := types[v.AssetID]; rt != "" && !slices.Contains(g.RecordTypes, rt) {
			g.RecordTypes = append(g.RecordTypes, rt)
		}
		if !slices.Contains(g.Sources, v.Source) {
			g.Sources = append(g.Sources, v.Source)
		}
	}
	return groups, nil
}

// Resolve under the same global asset-writer lock used by discovery, so group
// expansion and mutation cannot race a newly created DNS record.
func resolveApprovalSelection(tx *sql.Tx, taskID int64, ids []int64, keys []string) ([]int64, error) {
	if err := lockCompanyScopeMutation(tx); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return normalizeTaskAssetIDs(ids)
	}
	if len(ids) > 0 || len(keys) > MaxTaskAssetMutationCount {
		return nil, fmt.Errorf("%w: group_keys 与 asset_ids 二选一，最多%d组", ErrTaskAssetInvalid, MaxTaskAssetMutationCount)
	}
	for _, key := range keys {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 || parts[0] != strconv.FormatInt(taskID, 10) {
			return nil, fmt.Errorf("%w: 来源任务授权只读或分组无效", ErrTaskAssetInvalid)
		}
		var rows *sql.Rows
		var err error
		if host, ok := strings.CutPrefix(parts[1], "host:"); ok {
			rows, err = tx.Query(`SELECT a.id FROM assets a JOIN task_asset_links l ON l.asset_id=a.id WHERE l.task_id=$1 AND a.type IN ('root_domain','subdomain','ip') AND task_asset_host(a)=$2 ORDER BY a.id`, taskID, host)
		} else if value, ok := strings.CutPrefix(parts[1], "id:"); ok {
			id, e := strconv.ParseInt(value, 10, 64)
			if e != nil {
				return nil, fmt.Errorf("%w: invalid group", ErrTaskAssetInvalid)
			}
			rows, err = tx.Query(`SELECT asset_id FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, taskID, id)
		} else {
			return nil, fmt.Errorf("%w: invalid group", ErrTaskAssetInvalid)
		}
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
			count++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if count == 0 {
			return nil, ErrTaskAssetAssetNotFound
		}
	}
	return normalizeTaskAssetIDs(ids)
}
