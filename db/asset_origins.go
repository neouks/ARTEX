package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// AssetOrigin is a pointer into the original audit transcript, never a copy of it.
type AssetOrigin struct {
	TaskID     int64     `json:"task_id"`
	Session    string    `json:"session"`
	ToolUseID  string    `json:"tool_use_id"`
	ActivityID int64     `json:"activity_id"`
	AssetIDs   []int64   `json:"asset_ids,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Available  bool      `json:"available"`
}

func (s *AssetStore) setRegistrationOrigin(taskID int64, worker string, nodeID int64, toolUseID string) error {
	// Transaction-local settings are inherited by all host-creation triggers and
	// cannot leak through the connection pool or overwrite an existing link.
	var encoded string
	if taskID > 0 && toolUseID != "" {
		o := AssetOrigin{TaskID: taskID}
		var node sql.NullInt64
		var seg int
		var actor string
		err := s.tx.QueryRow(`SELECT a.id,COALESCE(a.worker,''),a.node_id,COALESCE(a.main_seg,0),a.created_at
FROM activity a JOIN tasks t ON t.exploration_id=a.exploration_id
WHERE t.id=$1 AND t.deleted_at IS NULL AND a.kind='tool_use' AND a.tool_use_id=$2
AND COALESCE(a.node_id,0)=$3 AND ($3>0 OR a.worker=$4)
ORDER BY a.id DESC LIMIT 1`, taskID, toolUseID, nodeID, worker).Scan(&o.ActivityID, &actor, &node, &seg, &o.CreatedAt)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			switch {
			case actor == "mainagent":
				o.Session = fmt.Sprintf("main:%d", seg)
			case actor == "planner":
				o.Session = "plan"
			case node.Valid:
				o.Session = fmt.Sprintf("intent:%d", node.Int64)
			}
			if o.Session != "" {
				o.ToolUseID = toolUseID
				b, err := json.Marshal(o)
				if err != nil {
					return err
				}
				encoded = string(b)
			}
		}
	}
	_, err := s.tx.Exec(`SELECT set_config('artex.asset_origin',$1,true),set_config('artex.origin_task',$2,true)`, encoded, fmt.Sprint(taskID))
	return err
}

func (s *AssetStore) approvalOrigins(rows []TaskAssetApproval) (map[string][]AssetOrigin, error) {
	out := map[string][]AssetOrigin{}
	tasks := []int64{}
	ids := []int64{}
	for _, r := range rows {
		tasks = append(tasks, r.SourceTaskID)
		ids = append(ids, r.AssetID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	results, err := s.query(`SELECT l.task_id,l.asset_id,l.source_origin,l.created_at,
EXISTS(SELECT 1 FROM activity a JOIN tasks t ON t.exploration_id=a.exploration_id
 WHERE t.id=l.task_id AND t.deleted_at IS NULL AND a.id=(l.source_origin->>'activity_id')::bigint
 AND a.kind='tool_use' AND a.tool_use_id=l.source_origin->>'tool_use_id')
FROM task_asset_links l WHERE l.task_id=ANY($1::bigint[]) AND l.asset_id=ANY($2::bigint[]) AND l.source_origin IS NOT NULL`, tasks, ids)
	if err != nil {
		return nil, err
	}
	defer results.Close()
	for results.Next() {
		var task, id int64
		var raw []byte
		var at time.Time
		var available bool
		if err := results.Scan(&task, &id, &raw, &at, &available); err != nil {
			return nil, err
		}
		var o AssetOrigin
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		o.Available = available
		o.AssetIDs = []int64{id}
		o.CreatedAt = at
		out[fmt.Sprintf("%d:%d", task, id)] = []AssetOrigin{o}
	}
	return out, results.Err()
}
