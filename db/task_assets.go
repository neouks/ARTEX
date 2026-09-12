package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxTaskAssetMutationCount = 100
	MaxTaskAssetSummaryRunes  = 500
	defaultTaskAssetSource    = "system"
	manualTaskScopeSummary    = "用户在测试资产页手工新增"
)

var (
	ErrTaskAssetInvalid       = errors.New("invalid task asset association")
	ErrTaskAssetTaskNotFound  = errors.New("task not found")
	ErrTaskAssetAssetNotFound = errors.New("asset not found")
	ErrTaskAssetBlocked       = errors.New("task asset is blocked")
	ErrTaskAssetNotApproved   = errors.New("task asset is not approved")
)

const (
	ApprovalApproved = "approved"
	ApprovalPending  = "pending"
	ApprovalRevoked  = "revoked"
	ApprovalBlocked  = "blocked"
)

// TaskAssetApproval is the task-local authorization record returned by the
// approval APIs and task-scoped asset queries.
type TaskAssetApproval struct {
	AssetID        int64      `json:"asset_id"`
	AssetType      string     `json:"asset_type"`
	Name           string     `json:"name"`
	Source         string     `json:"source"`
	SourceSummary  string     `json:"source_summary"`
	SourceNodeID   *int64     `json:"source_node_id,omitempty"`
	SourceTaskID   int64      `json:"source_task_id"`
	Inherited      bool       `json:"inherited"`
	ReadOnly       bool       `json:"read_only"`
	CreatedAt      time.Time  `json:"created_at"`
	ApprovalState  string     `json:"approval_state"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
	ApprovedBy     string     `json:"approved_by,omitempty"`
	ApprovalReason string     `json:"approval_reason,omitempty"`
	Blocked        bool       `json:"blocked"`
	BlockedAt      *time.Time `json:"blocked_at,omitempty"`
	BlockReason    string     `json:"block_reason,omitempty"`
	BlockedBy      string     `json:"blocked_by,omitempty"`
	BlockKind      string     `json:"block_kind,omitempty"`
	BlockDirect    bool       `json:"block_direct,omitempty"`
}

// AssetKey is a stable task-local identity used by deletion tombstones. It is
// deliberately independent from the global asset row id so deleting/recreating
// a shared asset cannot bypass a task's explicit block.
func AssetKey(asset *Asset) (key, host string) {
	if asset == nil {
		return "", ""
	}
	host = normalizeTaskAssetHost(asset.Domain)
	if host == "" {
		host = normalizeTaskAssetHost(asset.IP)
	}
	if host == "" && asset.URL != "" {
		if u, err := url.Parse(asset.URL); err == nil {
			host = normalizeTaskAssetHost(u.Hostname())
		}
	}
	switch asset.Type {
	case "root_domain", "subdomain":
		key = asset.Type + ":" + normalizeTaskAssetHost(asset.Domain)
	case "ip":
		key = "ip:" + normalizeTaskAssetHost(asset.IP)
	case "service":
		key = "service:" + normalizeURL(asset.URL)
		if asset.URL == "" {
			port := 0
			if asset.Port != nil {
				port = *asset.Port
			}
			key = "service:" + host + ":" + strconv.Itoa(port) + ":" + strings.ToLower(strings.TrimSpace(asset.ServiceName))
		}
	case "endpoint":
		key = "endpoint:" + normalizeURL(asset.URL) + ":" + strings.ToUpper(strings.TrimSpace(asset.Method))
	case "app":
		identity := strings.ToLower(strings.TrimSpace(asset.BundleID))
		if identity == "" {
			identity = strings.ToLower(strings.TrimSpace(asset.AppName))
		}
		key = "app:" + identity
	default:
		key = asset.Type + ":" + strings.TrimSpace(fmt.Sprint(asset.ID))
	}
	return key, host
}

func normalizeTaskAssetHost(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.Trim(value, "[]")
	if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
		value = parsed.Hostname()
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return strings.TrimSuffix(value, ".")
}

func taskAssetHostWithin(host, parent string) bool {
	host, parent = normalizeTaskAssetHost(host), normalizeTaskAssetHost(parent)
	if host == "" || parent == "" {
		return false
	}
	if net.ParseIP(parent) != nil {
		return host == parent
	}
	return host == parent || strings.HasSuffix(host, "."+parent)
}

func operatorApprovedTaskAssetSource(source string) bool {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case "manual", "direct", "company", "api", "task", "legacy":
		return true
	default:
		return false
	}
}

// TaskAssetMutation summarizes one attach request. Attached counts newly added
// associations; Existing counts requested assets that were already on the task.
type TaskAssetMutation struct {
	Requested int `json:"requested"`
	Attached  int `json:"attached"`
	Existing  int `json:"existing"`
}

// NormalizeTaskAssetIDs validates, de-duplicates, and bounds an optional list
// used during task creation. An empty list is valid; attach endpoints still use
// the stricter internal helper so an empty mutation remains an error there.
func NormalizeTaskAssetIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return normalizeTaskAssetIDs(ids)
}

// TaskAssetScopeMutation summarizes one free-form scope registration. Domain
// and IP entries create or reuse global assets; every entry also becomes an
// idempotent task_scope row.
type TaskAssetScopeMutation struct {
	Requested      int `json:"requested"`
	AssetsLinked   int `json:"assets_linked"`
	AssetsExisting int `json:"assets_existing"`
	ScopesAdded    int `json:"scopes_added"`
	ScopesExisting int `json:"scopes_existing"`
}

// IntentAsset describes an asset explicitly anchored to a worker intent.
type IntentAsset struct {
	IntentID      int64  `json:"intent_id"`
	AssetID       int64  `json:"asset_id"`
	Type          string `json:"type"`
	Label         string `json:"label"`
	Source        string `json:"source"`
	SourceSummary string `json:"source_summary"`
	SourceNodeID  *int64 `json:"source_node_id,omitempty"`
	SourceTaskID  int64  `json:"source_task_id"`
	Inherited     bool   `json:"inherited"`
}

func normalizeTaskAssetIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: asset_ids is required", ErrTaskAssetInvalid)
	}
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: asset id must be positive", ErrTaskAssetInvalid)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
		if len(normalized) > MaxTaskAssetMutationCount {
			return nil, fmt.Errorf("%w: at most %d assets per request", ErrTaskAssetInvalid, MaxTaskAssetMutationCount)
		}
	}
	return normalized, nil
}

func normalizeTaskAssetSource(source, summary string) (string, string, error) {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		source = defaultTaskAssetSource
	}
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) > MaxTaskAssetSummaryRunes {
		return "", "", fmt.Errorf("%w: source summary exceeds %d characters", ErrTaskAssetInvalid, MaxTaskAssetSummaryRunes)
	}
	return source, summary, nil
}

// SetTaskAssetSource improves the generic trigger-created provenance for one
// existing task association. It never creates or deletes an asset.
func (s *AssetStore) SetTaskAssetSource(taskID, assetID int64, source, summary string, sourceNodeID *int64) error {
	if taskID <= 0 || assetID <= 0 {
		return fmt.Errorf("%w: task and asset ids must be positive", ErrTaskAssetInvalid)
	}
	source, summary, err := normalizeTaskAssetSource(source, summary)
	if err != nil {
		return err
	}
	query := `
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary, source_node_id)
SELECT task.id, asset.id, $3, $4, $5
FROM tasks task
JOIN assets asset ON asset.id=$2 AND task.id=ANY(asset.task_ids)
WHERE task.id=$1 AND task.deleted_at IS NULL
ON CONFLICT (task_id, asset_id) DO UPDATE
SET source=CASE WHEN task_asset_links.source IN ('manual','direct','company','api','task','legacy')
                THEN task_asset_links.source ELSE EXCLUDED.source END,
    source_summary=CASE WHEN task_asset_links.source IN ('manual','direct','company','api','task','legacy')
                        THEN task_asset_links.source_summary ELSE EXCLUDED.source_summary END,
    source_node_id=CASE WHEN task_asset_links.source IN ('manual','direct','company','api','task','legacy')
                        THEN task_asset_links.source_node_id
                        ELSE COALESCE(EXCLUDED.source_node_id, task_asset_links.source_node_id) END`
	var result sql.Result
	if s.tx != nil {
		result, err = s.tx.Exec(query, taskID, assetID, source, summary, sourceNodeID)
	} else {
		result, err = s.db.Exec(query, taskID, assetID, source, summary, sourceNodeID)
	}
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%w: task or asset association does not exist", ErrTaskAssetInvalid)
	}
	return nil
}

// ValidateTaskAssetsApproved is the strict planning/startup admission check.
// Running Worker write-back uses ValidateWorkerAssets. Empty ids are valid for global directions. A
// current-task link is authoritative; when none exists, a directly inherited
// source link may authorize the asset using that source task's read-only state.
func (s *AssetStore) ValidateTaskAssetsApproved(taskID int64, assetIDs []int64) error {
	if taskID <= 0 || len(assetIDs) == 0 {
		return nil
	}
	ids, err := normalizeTaskAssetIDs(assetIDs)
	if err != nil {
		return err
	}
	states, err := s.TaskAssetApprovalStates(taskID, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		switch states[id] {
		case ApprovalApproved:
		case "":
			return ErrTaskAssetAssetNotFound
		case ApprovalBlocked:
			return fmt.Errorf("%w: asset %d 或其父资产已封禁", ErrTaskAssetBlocked, id)
		default:
			return fmt.Errorf("%w: asset %d 状态为 %s", ErrTaskAssetNotApproved, id, states[id])
		}
	}
	return nil
}

// TaskAssetApprovalStates batches effective authorization reads in one statement.
// Missing ids are omitted; callers must treat a missing state as unauthorized.
func (s *AssetStore) TaskAssetApprovalStates(taskID int64, ids []int64) (map[int64]string, error) {
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	rows, err := s.query(`SELECT id,task_asset_effective_approval_state($1,id)
FROM assets WHERE id=ANY($2::bigint[])`, taskID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[int64]string, len(ids))
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		states[id] = state
	}
	return states, rows.Err()
}

// FilterApprovedAssets removes task assets that are pending, revoked, or
// tombstoned. It is used by agent-facing list tools so hidden assets never
// enter Planner/Worker context even when a broad DSL query is used.
func (s *AssetStore) FilterApprovedAssets(taskID int64, assets []*Asset) ([]*Asset, error) {
	if taskID <= 0 || len(assets) == 0 {
		return assets, nil
	}
	ids := make([]int64, 0, len(assets))
	for _, asset := range assets {
		ids = append(ids, asset.ID)
	}
	states, err := s.TaskAssetApprovalStates(taskID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*Asset, 0, len(assets))
	for _, asset := range assets {
		if states[asset.ID] == "" {
			return nil, ErrTaskAssetAssetNotFound
		}
		if states[asset.ID] == ApprovalApproved {
			out = append(out, asset)
		}
	}
	return s.projectApprovedAssetHosts(taskID, out)
}

// A visible domain may contain a pending resolved IP (or CNAME). Strip those
// structured references from the model view without changing the stored asset.
func (s *AssetStore) projectApprovedAssetHosts(taskID int64, assets []*Asset) ([]*Asset, error) {
	unique := make(map[string]bool)
	add := func(host string) {
		if host != "" {
			unique[normalizeTaskAssetHost(host)] = true
		}
	}
	for _, a := range assets {
		add(a.IP)
		add(a.RootDomain)
		for _, host := range a.BoundDomains {
			add(host)
		}
		if a.RecordType == "A" || a.RecordType == "AAAA" || a.RecordType == "CNAME" {
			for _, host := range a.RecordValue {
				add(host)
			}
		}
	}
	hosts := make([]string, 0, len(unique))
	for host := range unique {
		hosts = append(hosts, host)
	}
	states, err := s.TaskHostApprovalStates(taskID, hosts)
	if err != nil {
		return nil, err
	}
	allowed := func(host string) bool {
		state := states[normalizeTaskAssetHost(host)]
		return state == ApprovalApproved || (s.workerRead && state == ApprovalPending)
	}
	filter := func(values []string) []string {
		var result []string
		for _, host := range values {
			if allowed(host) {
				result = append(result, host)
			}
		}
		return result
	}
	out := make([]*Asset, 0, len(assets))
	for _, asset := range assets {
		a := *asset
		if !allowed(a.IP) {
			a.IP = ""
		}
		if !allowed(a.RootDomain) {
			a.RootDomain = ""
		}
		a.BoundDomains = filter(a.BoundDomains)
		if a.RecordType == "A" || a.RecordType == "AAAA" || a.RecordType == "CNAME" {
			a.RecordValue = filter(a.RecordValue)
		}
		out = append(out, &a)
	}
	return out, nil
}

// ValidateTaskHostsApproved applies the same task authorization to unstructured
// network tools (Bash/HTTP/MCP). Explicit/related templates fail closed for an
// unknown host; all-assets still permits a new valid host unless an operator
// block, revocation, or deletion applies.
func (s *AssetStore) TaskHostApprovalStates(taskID int64, hosts []string) (map[string]string, error) {
	states := make(map[string]string, len(hosts))
	if len(hosts) == 0 {
		return states, nil
	}
	rows, err := s.query(`SELECT host,COALESCE(task_host_approval_state($1,host),'')
FROM unnest($2::text[]) AS requested(host)`, taskID, hosts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var host, state string
		if err := rows.Scan(&host, &state); err != nil {
			return nil, err
		}
		states[host] = state
	}
	return states, rows.Err()
}

func (s *AssetStore) ValidateTaskHostsApproved(taskID int64, hosts []string) error {
	if taskID <= 0 || len(hosts) == 0 {
		return nil
	}
	want := make([]string, 0, len(hosts))
	seen := make(map[string]bool)
	for _, host := range hosts {
		host = normalizeTaskAssetHost(host)
		if host == "" {
			continue
		}
		normalized, err := NormalizeAgentHost(host, true)
		if err != nil {
			return fmt.Errorf("%w: host %q 格式无效", ErrTaskAssetInvalid, host)
		}
		if !seen[normalized] {
			want = append(want, normalized)
			seen[normalized] = true
		}
	}
	states, err := s.TaskHostApprovalStates(taskID, want)
	if err != nil {
		return err
	}
	for _, host := range want {
		switch states[host] {
		case ApprovalApproved:
		case "":
			return ErrTaskAssetTaskNotFound
		case ApprovalBlocked:
			return fmt.Errorf("%w: host %s 已封禁", ErrTaskAssetBlocked, host)
		default:
			return fmt.Errorf("%w: host %s 状态为 %s", ErrTaskAssetNotApproved, host, states[host])
		}
	}
	return nil
}

func isParentAssetType(typ string) bool {
	return typ == "root_domain" || typ == "subdomain" || typ == "ip"
}

// RegisterAgentDiscoveredAsset is the compatibility path for callers that have
// already written a row. New discovery must use RegisterAgentAsset so writing
// the asset and deciding authorization are atomic.
func (s *AssetStore) RegisterAgentDiscoveredAsset(taskID, assetID int64, agentKey string) (string, error) {
	if taskID <= 0 || assetID <= 0 {
		return ApprovalApproved, nil
	}
	_, err := s.RegisterAgentAsset(taskID, agentKey, 0, func(scoped *AssetStore) (int64, error) {
		_, err := scoped.tx.Exec(`UPDATE task_asset_links l SET approval_state='pending',
approved_at=NULL,approved_by=NULL,approval_reason='Agent 发现，等待用户审批'
FROM assets a,assets discovered
WHERE discovered.id=$2 AND l.task_id=$1 AND a.id=l.asset_id
AND a.type IN ('root_domain','subdomain','ip')
AND l.source='system' AND l.approval_state='approved'
AND COALESCE(l.approved_by,'')='' AND COALESCE(l.approval_reason,'')=''
AND (a.id=$2 OR task_asset_host(a)=task_asset_host(discovered)
 OR (discovered.root_domain<>'' AND a.domain=discovered.root_domain))`, taskID, assetID)
		return assetID, err
	})
	if err != nil {
		return "", err
	}
	states, err := s.TaskAssetApprovalStates(taskID, []int64{assetID})
	if err != nil {
		return "", err
	}
	state := states[assetID]
	switch state {
	case "":
		return "", ErrTaskAssetAssetNotFound
	case ApprovalBlocked:
		return state, ErrTaskAssetBlocked
	case ApprovalRevoked:
		return state, ErrTaskAssetNotApproved
	default:
		return state, nil
	}
}

func rejectDerivedApprovalAssets(tx *sql.Tx, ids []int64) error {
	var derived bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM assets WHERE id=ANY($1::bigint[]) AND type NOT IN ('root_domain','subdomain','ip'))`, ids).Scan(&derived); err != nil {
		return err
	}
	if derived {
		return fmt.Errorf("%w: 服务和接口无需单独审批，请操作父域名/IP", ErrTaskAssetInvalid)
	}
	return nil
}

// ApproveTaskAssets changes only the current task's links and removes matching
// tombstones, restoring a manually reattached asset's test eligibility.
func (s *AssetStore) ApproveTaskAssets(taskID int64, assetIDs []int64, actor, reason string, groupKeys ...string) error {
	_, err := s.ApproveTaskAssetsResolved(taskID, assetIDs, actor, reason, groupKeys...)
	return err
}

func (s *AssetStore) ApproveTaskAssetsResolved(taskID int64, assetIDs []int64, actor, reason string, groupKeys ...string) ([]int64, error) {
	ids, err := normalizeTaskAssetIDs(assetIDs)
	if err != nil && len(groupKeys) == 0 {
		return nil, err
	}
	actor, reason = strings.TrimSpace(actor), strings.TrimSpace(reason)
	if actor == "" {
		actor = "user"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	ids, err = resolveApprovalSelection(tx, taskID, assetIDs, groupKeys)
	if err != nil {
		return nil, err
	}
	if err := rejectDerivedApprovalAssets(tx, ids); err != nil {
		return nil, err
	}
	assets, err := lockTaskAssetApprovalRows(tx, taskID, ids)
	if err != nil {
		return nil, err
	}
	if err := rejectDeletedApprovalAssets(tx, taskID, ids); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE task_asset_links SET approval_state='approved', approved_at=now(), approved_by=$3,
	approval_reason=$4 WHERE task_id=$1 AND asset_id=ANY($2::bigint[])`, taskID, ids, actor, reason); err != nil {
		return nil, err
	}
	for _, id := range ids {
		asset := assets[id]
		key, _ := AssetKey(asset)
		if _, err = tx.Exec(`DELETE FROM task_asset_blocks WHERE task_id=$1 AND block_kind='manual' AND (asset_id=$2 OR asset_key=$3)`, taskID, id, key); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *AssetStore) RevokeTaskAssets(taskID int64, assetIDs []int64, actor, reason string, groupKeys ...string) error {
	_, err := s.RevokeTaskAssetsResolved(taskID, assetIDs, actor, reason, groupKeys...)
	return err
}

func (s *AssetStore) RevokeTaskAssetsResolved(taskID int64, assetIDs []int64, actor, reason string, groupKeys ...string) ([]int64, error) {
	ids, err := normalizeTaskAssetIDs(assetIDs)
	if err != nil && len(groupKeys) == 0 {
		return nil, err
	}
	actor, reason = strings.TrimSpace(actor), strings.TrimSpace(reason)
	if actor == "" {
		actor = "user"
	}
	if reason == "" {
		reason = "用户撤回测试授权"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	ids, err = resolveApprovalSelection(tx, taskID, assetIDs, groupKeys)
	if err != nil {
		return nil, err
	}
	if err := rejectDerivedApprovalAssets(tx, ids); err != nil {
		return nil, err
	}
	_, err = lockTaskAssetApprovalRows(tx, taskID, ids)
	if err != nil {
		return nil, err
	}
	var blocked bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM unnest($2::bigint[]) id WHERE task_asset_blocked($1,id))`, taskID, ids).Scan(&blocked); err != nil {
		return nil, err
	}
	if blocked {
		return nil, fmt.Errorf("%w: 封禁资产需先批准或重新关联，不能直接撤回", ErrTaskAssetBlocked)
	}
	if _, err = tx.Exec(`UPDATE task_asset_links SET approval_state='revoked', approval_reason=$3,
	approved_by=$4 WHERE task_id=$1 AND asset_id=ANY($2::bigint[])`, taskID, ids, reason, actor); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func lockTaskAssetApprovalRows(tx *sql.Tx, taskID int64, ids []int64) (map[int64]*Asset, error) {
	var taskExists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, taskID).Scan(&taskExists); err != nil {
		return nil, err
	}
	if !taskExists {
		return nil, ErrTaskAssetTaskNotFound
	}
	rows, err := tx.Query(`SELECT a.id,a.type,COALESCE(a.domain,''),COALESCE(a.root_domain,''),
COALESCE(a.ip,''),COALESCE(a.url,''),COALESCE(a.method,''),COALESCE(a.service_name,''),COALESCE(a.port,0)
FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
WHERE l.task_id=$1 AND l.asset_id=ANY($2::bigint[]) FOR UPDATE OF l`, taskID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets := make(map[int64]*Asset, len(ids))
	for rows.Next() {
		var asset Asset
		if err := rows.Scan(&asset.ID, &asset.Type, &asset.Domain, &asset.RootDomain, &asset.IP,
			&asset.URL, &asset.Method, &asset.ServiceName, &asset.Port); err != nil {
			return nil, err
		}
		assets[asset.ID] = &asset
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(assets) != len(ids) {
		return nil, ErrTaskAssetAssetNotFound
	}
	return assets, nil
}

// RunningIntentIDsForAssets returns current-task running intents anchored to
// the supplied assets. Callers use it to cancel only affected Workers.
func (s *AssetStore) RunningIntentIDsForAssets(taskID int64, assetIDs []int64) ([]int64, error) {
	ids, err := normalizeTaskAssetIDs(assetIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(`WITH selected AS (
 SELECT task_asset_host(assets) AS host FROM assets WHERE id=ANY($2::bigint[]) AND type IN ('root_domain','subdomain','ip')
), targets AS (
 SELECT a.id FROM assets a WHERE a.id=ANY($2::bigint[]) AND a.type IN ('root_domain','subdomain','ip')
 UNION SELECT child.id FROM assets child JOIN selected s ON s.host<>'' AND (
   task_asset_host_within(task_asset_host(child),s.host))
)
SELECT DISTINCT n.id
FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id AND n.kind='intent' AND n.state='running'
JOIN exploration_anchors ea ON ea.node_id=n.id
JOIN targets target ON target.id=ea.asset_id
WHERE t.id=$1
UNION
SELECT DISTINCT access.intent_id FROM task_worker_asset_access access
JOIN tasks t ON t.id=access.task_id
JOIN exploration_nodes n ON n.id=access.intent_id AND n.exploration_id=t.exploration_id AND n.state='running'
WHERE access.task_id=$1 AND (access.asset_id IN (SELECT id FROM targets)
 OR EXISTS(SELECT 1 FROM selected WHERE host<>'' AND task_asset_host_within(access.host,selected.host)))`, taskID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListTaskAssetApprovals returns all links plus tombstone-only rows for the
// current task. It is intentionally task-scoped and never used by global assets.
func (s *AssetStore) ListTaskAssetApprovals(taskID int64) ([]TaskAssetApproval, error) {
	rows, err := s.query(`WITH inherited_link AS (
  SELECT DISTINCT ON (l.asset_id)
         l.asset_id,l.source,l.source_summary,l.source_node_id,
         l.task_id AS source_task_id,l.created_at,task_asset_owner_approval_state(l.task_id,l.asset_id) AS approval_state,
         l.approved_at,l.approved_by,l.approval_reason
  FROM task_relations relation
  JOIN task_asset_links l ON l.task_id=relation.source_task_id
  WHERE relation.task_id=$1
    AND NOT EXISTS (
      SELECT 1 FROM task_asset_links current_link
      WHERE current_link.task_id=$1 AND current_link.asset_id=l.asset_id
    )
  ORDER BY l.asset_id,
           CASE task_asset_owner_approval_state(l.task_id,l.asset_id) WHEN 'approved' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END,
           l.task_id
)
SELECT a.id,a.type,
COALESCE(NULLIF(a.domain,''),NULLIF(a.ip,''),NULLIF(a.url,''),NULLIF(a.app_name,''),'#'||a.id::text),
l.source,l.source_summary,l.source_node_id,l.task_id,false,false,l.created_at,task_asset_owner_approval_state(l.task_id,l.asset_id),
l.approved_at,COALESCE(l.approved_by,''),COALESCE(l.approval_reason,''),false,NULL::timestamptz,'','',''
FROM task_asset_links l JOIN assets a ON a.id=l.asset_id WHERE l.task_id=$1 AND a.type IN ('root_domain','subdomain','ip')
UNION ALL
SELECT a.id,a.type,
COALESCE(NULLIF(a.domain,''),NULLIF(a.ip,''),NULLIF(a.url,''),NULLIF(a.app_name,''),'#'||a.id::text),
inherited.source,inherited.source_summary,inherited.source_node_id,inherited.source_task_id,true,true,inherited.created_at,inherited.approval_state,
inherited.approved_at,COALESCE(inherited.approved_by,''),COALESCE(inherited.approval_reason,''),false,NULL::timestamptz,'','',''
FROM inherited_link inherited
JOIN assets a ON a.id=inherited.asset_id WHERE a.type IN ('root_domain','subdomain','ip')
UNION ALL
SELECT COALESCE(b.asset_id,0),b.asset_type,b.asset_key,'deleted',b.reason,NULL,$1::bigint,false,true,b.blocked_at,
'blocked',NULL,'',b.reason,true,b.blocked_at,b.reason,b.blocked_by,b.block_kind
FROM task_asset_blocks b WHERE b.task_id=$1 AND b.asset_type IN ('root_domain','subdomain','ip') AND NOT EXISTS (
 SELECT 1 FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
 WHERE l.task_id=b.task_id AND (l.asset_id=b.asset_id OR task_asset_identity_key(a)=b.asset_key))
  AND NOT EXISTS (
    SELECT 1 FROM task_relations relation JOIN task_asset_links source_link ON source_link.task_id=relation.source_task_id
    JOIN assets a ON a.id=source_link.asset_id
    WHERE relation.task_id=b.task_id AND (source_link.asset_id=b.asset_id OR task_asset_identity_key(a)=b.asset_key)
  )
ORDER BY 8,1,7`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskAssetApproval{}
	for rows.Next() {
		var v TaskAssetApproval
		var sourceNodeID sql.NullInt64
		if err := rows.Scan(&v.AssetID, &v.AssetType, &v.Name, &v.Source, &v.SourceSummary, &sourceNodeID,
			&v.SourceTaskID, &v.Inherited, &v.ReadOnly, &v.CreatedAt, &v.ApprovalState, &v.ApprovedAt, &v.ApprovedBy, &v.ApprovalReason,
			&v.Blocked, &v.BlockedAt, &v.BlockReason, &v.BlockedBy, &v.BlockKind); err != nil {
			return nil, err
		}
		if sourceNodeID.Valid {
			id := sourceNodeID.Int64
			v.SourceNodeID = &id
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Most tasks have no tombstones. Avoid one detail query per approval row
	// (and another per inherited row) in that common case.
	var hasBlocks bool
	if err := s.queryRow(`SELECT EXISTS(SELECT 1 FROM task_asset_blocks
WHERE task_id=$1 OR task_id IN (SELECT source_task_id FROM task_relations WHERE task_id=$1))`, taskID).Scan(&hasBlocks); err != nil {
		return nil, err
	}
	if !hasBlocks {
		return out, nil
	}
	for i := range out {
		if out[i].Blocked || out[i].AssetID <= 0 {
			continue
		}
		blocked, at, reason, actor, err := s.taskAssetBlockInfo(taskID, out[i].AssetID)
		if err != nil {
			return nil, err
		}
		if !blocked && out[i].Inherited {
			blocked, at, reason, actor, err = s.taskAssetBlockInfo(out[i].SourceTaskID, out[i].AssetID)
			if err != nil {
				return nil, err
			}
		}
		if blocked {
			out[i].Blocked = true
			out[i].ApprovalState = ApprovalBlocked
			out[i].BlockedAt = at
			out[i].BlockReason = reason
			out[i].BlockedBy = actor
			kind, err := s.directTaskAssetBlockKind(taskID, out[i].AssetID)
			if err != nil {
				return nil, err
			}
			out[i].BlockKind = kind
			out[i].BlockDirect = kind != ""
		}
	}
	return out, nil
}

func (s *AssetStore) taskAssetBlockInfo(taskID, assetID int64) (bool, *time.Time, string, string, error) {
	var at time.Time
	var reason, actor string
	err := s.queryRow(`SELECT b.blocked_at,b.reason,b.blocked_by
FROM task_asset_blocks b JOIN assets a ON a.id=$2
WHERE b.task_id=$1 AND (b.asset_type IN ('root_domain','subdomain','ip') OR b.block_kind='invalid')
AND (b.asset_id=a.id OR b.asset_key=task_asset_identity_key(a)
 OR (b.asset_type IN ('root_domain','subdomain','ip') AND task_asset_host_within(task_asset_host(a),b.host_key)))
ORDER BY b.blocked_at DESC LIMIT 1`, taskID, assetID).Scan(&at, &reason, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, "", "", nil
	}
	if err != nil {
		return false, nil, "", "", err
	}
	return true, &at, reason, actor, nil
}

// MarkTaskAssetsTested records that the current task's agent produced a fact
// or finding for these assets. The update is task-local, so shared global asset
// rows never leak test state across tasks.
func (s *AssetStore) MarkTaskAssetsTested(taskID int64, assetIDs []int64, agentKey string) error {
	if taskID <= 0 || len(assetIDs) == 0 {
		return nil
	}
	ids, err := normalizeTaskAssetIDs(assetIDs)
	if err != nil {
		return err
	}
	query := `UPDATE task_asset_links
SET tested=true, tested_at=COALESCE(tested_at,now()), tested_by=COALESCE(NULLIF($3,''),tested_by)
	WHERE task_id=$1 AND asset_id=ANY($2::bigint[]) AND task_asset_effectively_approved(task_id,asset_id)`
	if s.tx != nil {
		_, err = s.tx.Exec(query, taskID, ids, strings.TrimSpace(agentKey))
	} else {
		_, err = s.db.Exec(query, taskID, ids, strings.TrimSpace(agentKey))
	}
	return err
}

// RegisterTaskAssetScopes accepts the same structured scope rules as enterprise
// assets. The entire request is atomic: invalid input or any storage failure
// leaves both global assets and task scope unchanged.
func (s *AssetStore) RegisterTaskAssetScopes(taskID int64, inputs []ScopeInput) (TaskAssetScopeMutation, error) {
	mutation := TaskAssetScopeMutation{Requested: len(inputs)}
	if taskID <= 0 {
		return mutation, fmt.Errorf("%w: task id must be positive", ErrTaskAssetInvalid)
	}
	if len(inputs) == 0 {
		return mutation, fmt.Errorf("%w: scope is required", ErrTaskAssetInvalid)
	}
	if err := ValidateCompanyScopeInputBounds(inputs); err != nil {
		return mutation, fmt.Errorf("%w: %v", ErrTaskAssetInvalid, err)
	}
	parsed := make([]ParsedScope, 0, len(inputs))
	for index, input := range inputs {
		input.Manual = true
		rule, err := ParseScopeInput(input)
		if err != nil {
			return mutation, fmt.Errorf("%w: 第 %d 条范围无效: %v", ErrTaskAssetInvalid, index+1, err)
		}
		parsed = append(parsed, rule)
	}
	if err := validateParsedScopeBounds(parsed); err != nil {
		return mutation, fmt.Errorf("%w: %v", ErrTaskAssetInvalid, err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return mutation, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockCompanyScopeMutation(tx); err != nil {
		return mutation, err
	}
	var taskExists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, taskID).Scan(&taskExists); err != nil {
		return mutation, err
	}
	if !taskExists {
		return mutation, ErrTaskAssetTaskNotFound
	}

	scoped := &AssetStore{db: s.db, company: s.company, tx: tx}
	if _, err := tx.Exec(`SELECT set_config('artex.user_asset_registration','on',true)`); err != nil {
		return mutation, err
	}
	for _, rule := range parsed {
		taskScope := TaskScope{
			TaskID: taskID,
			Source: "manual",
			Reason: manualTaskScopeSummary,
		}
		var assetID int64
		switch rule.Kind {
		case "domain":
			taskScope.Kind = "subdomain"
			if strings.HasPrefix(strings.TrimSpace(rule.Raw), "*.") {
				taskScope.Kind = "root_domain"
			}
			taskScope.Domain = rule.Domain
			var alreadyLinked bool
			err := tx.QueryRow(`SELECT id, $2=ANY(task_ids) FROM assets WHERE type='root_domain' AND domain=$1`, rule.Domain, taskID).
				Scan(&assetID, &alreadyLinked)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return mutation, err
			}
			assetID, err = scoped.UpsertRootDomain(UpsertRootDomainReq{Domain: rule.Domain, TaskID: taskID})
			if err != nil {
				return mutation, err
			}
			if alreadyLinked {
				mutation.AssetsExisting++
			} else {
				mutation.AssetsLinked++
			}
		case "ip":
			taskScope.Kind = "ip"
			taskScope.Net = rule.Net
			ip, _, parseErr := net.ParseCIDR(rule.Net)
			if parseErr != nil {
				return mutation, fmt.Errorf("%w: 无效 IP: %s", ErrTaskAssetInvalid, rule.Raw)
			}
			ipValue := ip.String()
			var alreadyLinked bool
			err := tx.QueryRow(`SELECT id, $2=ANY(task_ids) FROM assets WHERE type='ip' AND ip=$1`, ipValue, taskID).
				Scan(&assetID, &alreadyLinked)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return mutation, err
			}
			assetID, err = scoped.UpsertIP(UpsertIPReq{IP: ipValue, TaskID: taskID})
			if err != nil {
				return mutation, err
			}
			if alreadyLinked {
				mutation.AssetsExisting++
			} else {
				mutation.AssetsLinked++
			}
		case "cidr":
			taskScope.Kind = "cidr"
			taskScope.Net = rule.Net
		case "icp", "keyword":
			taskScope.Kind = rule.Kind
			taskScope.Value = rule.Value
			if u, e := url.Parse(rule.Value); e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" {
				assetID, err = scoped.UpsertHTTPService(UpsertHTTPServiceReq{URL: rule.Value, TaskID: taskID})
				if err != nil {
					return mutation, err
				}
				if err = authorizeUserAsset(scoped, taskID, assetID, "manual", manualTaskScopeSummary, true); err != nil {
					return mutation, err
				}
			}
		default:
			return mutation, fmt.Errorf("%w: unsupported scope kind %q", ErrTaskAssetInvalid, rule.Kind)
		}

		if assetID > 0 {
			if err := scoped.SetTaskAssetSource(taskID, assetID, "manual", manualTaskScopeSummary, nil); err != nil {
				return mutation, err
			}
			if _, err := tx.Exec(`UPDATE task_asset_links SET approval_state='approved', approved_at=now(), approved_by='user', approval_reason='手动范围登记' WHERE task_id=$1 AND asset_id=$2`, taskID, assetID); err != nil {
				return mutation, err
			}
			assetKey := ""
			if rule.Kind == "domain" {
				assetKey = "root_domain:" + strings.TrimSuffix(strings.ToLower(rule.Domain), ".")
			} else if rule.Kind == "ip" {
				assetKey = "ip:" + taskScope.Net
				if ip, _, parseErr := net.ParseCIDR(taskScope.Net); parseErr == nil {
					assetKey = "ip:" + ip.String()
				}
			}
			if _, err := tx.Exec(`DELETE FROM task_asset_blocks WHERE task_id=$1 AND block_kind<>'invalid' AND (asset_id=$2 OR asset_key=$3)`, taskID, assetID, assetKey); err != nil {
				return mutation, err
			}
		}
		inserted, err := scoped.upsertTaskScopeResult(taskScope)
		if err != nil {
			return mutation, err
		}
		if err := scoped.authorizeScopeAssetsInTx(tx, taskScope); err != nil {
			return mutation, err
		}
		if inserted {
			mutation.ScopesAdded++
		} else {
			mutation.ScopesExisting++
		}
	}
	if err := seedUserAssetGrants(tx, taskID); err != nil {
		return mutation, err
	}
	if err := tx.Commit(); err != nil {
		return mutation, err
	}
	return mutation, nil
}

// AttachAssetsToTask associates existing global assets with one live task and
// records an operator-authored source summary. Global asset rows are retained.
func (s *AssetStore) AttachAssetsToTask(taskID int64, assetIDs []int64, sourceSummary string) (TaskAssetMutation, error) {
	var mutation TaskAssetMutation
	assetIDs, err := normalizeTaskAssetIDs(assetIDs)
	if err != nil {
		return mutation, err
	}
	_, sourceSummary, err = normalizeTaskAssetSource("manual", sourceSummary)
	if err != nil {
		return mutation, err
	}
	if sourceSummary == "" {
		return mutation, fmt.Errorf("%w: source_summary is required", ErrTaskAssetInvalid)
	}
	mutation.Requested = len(assetIDs)
	tx, err := s.db.Begin()
	if err != nil {
		return mutation, err
	}
	defer tx.Rollback() //nolint:errcheck

	var taskExists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, taskID).Scan(&taskExists); err != nil {
		return mutation, err
	}
	if !taskExists {
		return mutation, ErrTaskAssetTaskNotFound
	}
	var found, existing int
	if err := tx.QueryRow(`
SELECT count(*), count(*) FILTER (WHERE $1=ANY(task_ids))
FROM assets WHERE id=ANY($2::bigint[])`, taskID, assetIDs).Scan(&found, &existing); err != nil {
		return mutation, err
	}
	if found != len(assetIDs) {
		return mutation, ErrTaskAssetAssetNotFound
	}
	// Order matters: this UPDATE fires trg_assets_task_links, which creates the
	// link rows with the generic source='system'. The INSERT below must stay
	// after it so the operator-authored 'manual' provenance wins; swapping the
	// two statements silently degrades every manual attach back to 'system'.
	if _, err := tx.Exec(`
UPDATE assets
SET task_ids=CASE WHEN $1=ANY(task_ids) THEN task_ids ELSE array_append(task_ids,$1) END
WHERE id=ANY($2::bigint[])`, taskID, assetIDs); err != nil {
		return mutation, err
	}
	if _, err := tx.Exec(`
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
SELECT $1, id, 'manual', $3 FROM assets WHERE id=ANY($2::bigint[])
ON CONFLICT (task_id, asset_id) DO UPDATE
SET source='manual', source_summary=EXCLUDED.source_summary, source_node_id=NULL,
    approval_state='approved', approved_at=now(), approved_by='user', approval_reason='手动关联'`,
		taskID, assetIDs, sourceSummary); err != nil {
		return mutation, err
	}
	if _, err := tx.Exec(`DELETE FROM task_asset_blocks WHERE task_id=$1 AND block_kind<>'invalid' AND asset_id=ANY($2::bigint[])`, taskID, assetIDs); err != nil {
		return mutation, err
	}
	// Clear tombstones by normalized identity as well as id so a globally
	// deleted/recreated asset can be manually reattached to regain permission.
	blockRows, err := tx.Query(`SELECT id,type,COALESCE(domain,''),COALESCE(ip,''),COALESCE(url,''),COALESCE(method,''),COALESCE(service_name,''),COALESCE(port,0) FROM assets WHERE id=ANY($1::bigint[])`, assetIDs)
	if err != nil {
		return mutation, err
	}
	var blockKeys []string
	for blockRows.Next() {
		var a Asset
		if err := blockRows.Scan(&a.ID, &a.Type, &a.Domain, &a.IP, &a.URL, &a.Method, &a.ServiceName, &a.Port); err != nil {
			blockRows.Close()
			return mutation, err
		}
		key, _ := AssetKey(&a)
		blockKeys = append(blockKeys, key)
	}
	if err := blockRows.Close(); err != nil {
		return mutation, err
	}
	for _, key := range blockKeys {
		if _, err := tx.Exec(`DELETE FROM task_asset_blocks WHERE task_id=$1 AND block_kind<>'invalid' AND asset_key=$2`, taskID, key); err != nil {
			return mutation, err
		}
	}
	scoped := &AssetStore{db: s.db, company: s.company, tx: tx}
	if err := scoped.ensureManualDerivedParents(taskID, assetIDs); err != nil {
		return mutation, err
	}
	mutation.Existing = existing
	mutation.Attached = len(assetIDs) - existing
	return mutation, tx.Commit()
}

// DetachAssetFromTask removes only the task association and records a task-local
// deletion tombstone. The global asset and exploration anchors remain available
// for historical blackboard auditing.
func (s *AssetStore) DetachAssetFromTask(taskID, assetID int64) (bool, error) {
	if taskID <= 0 || assetID <= 0 {
		return false, fmt.Errorf("%w: task and asset ids must be positive", ErrTaskAssetInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	var asset Asset
	var local, inherited bool
	err = tx.QueryRow(`SELECT a.id,a.type,COALESCE(a.domain,''),COALESCE(a.root_domain,''),COALESCE(a.ip,''),
COALESCE(a.url,''),COALESCE(a.method,''),COALESCE(a.service_name,''),COALESCE(a.port,0),
EXISTS (SELECT 1 FROM task_asset_links link WHERE link.task_id=$1 AND link.asset_id=a.id),
EXISTS (SELECT 1 FROM task_relations relation JOIN task_asset_links source_link
        ON source_link.task_id=relation.source_task_id AND source_link.asset_id=a.id
        WHERE relation.task_id=$1)
FROM assets a WHERE a.id=$2 FOR UPDATE`, taskID, assetID).
		Scan(&asset.ID, &asset.Type, &asset.Domain, &asset.RootDomain, &asset.IP, &asset.URL, &asset.Method, &asset.ServiceName, &asset.Port, &local, &inherited)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !local && !inherited {
		return false, nil
	}
	key, host := AssetKey(&asset)
	if _, err = tx.Exec(`INSERT INTO task_asset_blocks(task_id,asset_key,asset_type,host_key,asset_id,reason,blocked_by)
VALUES ($1,$2,$3,$4,$5,'用户从当前任务删除','user')
ON CONFLICT (task_id,asset_key) DO UPDATE SET reason=EXCLUDED.reason,blocked_at=now(),blocked_by=EXCLUDED.blocked_by,block_kind='deleted'`, taskID, key, asset.Type, host, assetID); err != nil {
		return false, err
	}
	// Non-host deletion is a presentation exclusion, not execution revocation.
	// Keep its lineage association so a running intent can still write results.
	if local && isParentAssetType(asset.Type) {
		if _, err = tx.Exec(`UPDATE assets SET task_ids=array_remove(task_ids,$1) WHERE id=$2`, taskID, assetID); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func (s *AssetStore) hydrateTaskAssetSources(taskID int64, assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(assets))
	byID := make(map[int64]*Asset, len(assets))
	for _, asset := range assets {
		ids = append(ids, asset.ID)
		byID[asset.ID] = asset
	}
	rows, err := s.query(`WITH candidates AS (
  SELECT link.asset_id,link.task_id AS source_task_id,false AS inherited,
         link.source,link.source_summary,link.source_node_id,link.tested,link.tested_at,
         COALESCE(link.tested_by,'') AS tested_by,task_asset_owner_approval_state(link.task_id,link.asset_id) AS approval_state,link.approved_at,
         COALESCE(link.approved_by,'') AS approved_by,COALESCE(link.approval_reason,'') AS approval_reason
  FROM task_asset_links link
  WHERE link.task_id=$1 AND link.asset_id=ANY($2::bigint[])
  UNION ALL
  SELECT source_link.asset_id,source_link.task_id,true,
         source_link.source,source_link.source_summary,source_link.source_node_id,source_link.tested,source_link.tested_at,
         COALESCE(source_link.tested_by,''),task_asset_owner_approval_state(source_link.task_id,source_link.asset_id),source_link.approved_at,
         COALESCE(source_link.approved_by,''),COALESCE(source_link.approval_reason,'')
  FROM task_relations relation
  JOIN task_asset_links source_link ON source_link.task_id=relation.source_task_id
  WHERE relation.task_id=$1 AND source_link.asset_id=ANY($2::bigint[])
    AND NOT EXISTS (SELECT 1 FROM task_asset_links current_link
                    WHERE current_link.task_id=$1 AND current_link.asset_id=source_link.asset_id)
), ranked AS (
  SELECT candidates.*,
         row_number() OVER (
           PARTITION BY asset_id
           ORDER BY inherited,
                    CASE WHEN approval_state='approved' THEN 0
                         WHEN approval_state='pending' THEN 1 ELSE 2 END,
                    source_task_id
         ) AS ordinal
  FROM candidates
)
SELECT asset_id,source_task_id,inherited,source,source_summary,source_node_id,tested,tested_at,tested_by,
       approval_state,approved_at,approved_by,approval_reason
FROM ranked WHERE ordinal=1`, taskID, ids)
	if err != nil {
		return err
	}
	ownerByAsset := make(map[int64]int64, len(assets))
	for rows.Next() {
		var assetID, sourceTaskID int64
		var inherited bool
		var source, summary string
		var sourceNodeID sql.NullInt64
		var tested bool
		var testedAt sql.NullTime
		var testedBy string
		var approvalState, approvedBy, approvalReason string
		var approvedAt sql.NullTime
		if err := rows.Scan(&assetID, &sourceTaskID, &inherited, &source, &summary, &sourceNodeID,
			&tested, &testedAt, &testedBy, &approvalState, &approvedAt, &approvedBy, &approvalReason); err != nil {
			rows.Close()
			return err
		}
		if asset := byID[assetID]; asset != nil {
			ownerByAsset[assetID] = sourceTaskID
			asset.TaskSource = source
			asset.TaskSourceSummary = summary
			asset.TaskSourceTaskID = sourceTaskID
			asset.TaskInherited = inherited
			asset.TaskReadOnly = inherited
			asset.Tested = &tested
			if testedAt.Valid {
				t := testedAt.Time
				asset.TestedAt = &t
			}
			asset.TestedBy = testedBy
			asset.ApprovalState = approvalState
			asset.ApprovedBy = approvedBy
			asset.ApprovalReason = approvalReason
			if approvedAt.Valid {
				t := approvedAt.Time
				asset.ApprovedAt = &t
			}
			if sourceNodeID.Valid {
				id := sourceNodeID.Int64
				asset.TaskSourceNodeID = &id
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	type blockRecord struct {
		key, assetType, host, reason, kind string
		at                                 time.Time
	}
	blockRows, err := s.query(`SELECT asset_key,asset_type,host_key,blocked_at,reason,block_kind FROM task_asset_blocks WHERE task_id=$1 AND (asset_type IN ('root_domain','subdomain','ip') OR block_kind='invalid')`, taskID)
	if err != nil {
		return err
	}
	var blocks []blockRecord
	for blockRows.Next() {
		var block blockRecord
		if err := blockRows.Scan(&block.key, &block.assetType, &block.host, &block.at, &block.reason, &block.kind); err != nil {
			blockRows.Close()
			return err
		}
		blocks = append(blocks, block)
	}
	if err := blockRows.Err(); err != nil {
		blockRows.Close()
		return err
	}
	if err := blockRows.Close(); err != nil {
		return err
	}
	for _, asset := range assets {
		key, host := AssetKey(asset)
		var matched *blockRecord
		for i := range blocks {
			block := &blocks[i]
			if key == block.key {
				matched = block
				asset.BlockDirect = true
				break
			}
			if matched == nil && isParentAssetType(block.assetType) && taskAssetHostWithin(host, block.host) {
				matched = block
			}
		}
		if matched != nil {
			asset.Blocked = true
			asset.BlockReason = matched.reason
			asset.ApprovalState = ApprovalBlocked
			asset.BlockKind = matched.kind
			at := matched.at
			asset.BlockedAt = &at
		}
		if !asset.Blocked && asset.TaskInherited {
			blocked, at, reason, _, err := s.taskAssetBlockInfo(asset.TaskSourceTaskID, asset.ID)
			if err != nil {
				return err
			}
			if blocked {
				asset.Blocked, asset.BlockedAt, asset.BlockReason = true, at, reason
			}
		}
		if asset.ApprovalState == "blocked" {
			asset.Blocked = true
			if asset.BlockReason == "" {
				asset.BlockReason = "资产或父域名/IP已封禁"
			}
		}
		if asset.Type == "service" || asset.Type == "endpoint" {
			asset.ApprovedAt, asset.ApprovedBy = nil, ""
			switch asset.ApprovalState {
			case ApprovalPending:
				asset.ApprovalReason = "父资产待审批或缺少可确认父主机"
			case ApprovalRevoked, ApprovalBlocked:
				asset.ApprovalReason = "父资产已撤回或封禁"
			default:
				asset.ApprovalReason = "继承父资产授权"
			}
		}
		if _, hasLink := ownerByAsset[asset.ID]; !hasLink && asset.Blocked {
			tested := false
			asset.Tested = &tested
			asset.TaskSource = "deleted"
			asset.TaskSourceSummary = asset.BlockReason
			asset.TaskSourceTaskID = taskID
			asset.TaskReadOnly = true
		}
	}
	return nil
}

// IntentAssets returns all local worker targets plus immutable targets from the
// task's direct sources. Inherited non-terminal intents remain hidden, matching
// the existing source-aware session contract.
func (s *AssetStore) IntentAssets(taskID int64) ([]IntentAsset, error) {
	return s.IntentAssetsPage(taskID, 0, 0)
}

// IntentAssetsPage returns mappings for a newest-first page of intents. A zero
// limit preserves the legacy unbounded behavior for internal callers.
func (s *AssetStore) IntentAssetsPage(taskID, before int64, limit int) ([]IntentAsset, error) {
	rows, err := s.query(`
WITH context AS (
    SELECT task.id AS task_id, task.exploration_id, false AS inherited
    FROM tasks task
    WHERE task.id=$1 AND task.deleted_at IS NULL
    UNION ALL
    SELECT source.id, source.exploration_id, true
    FROM task_relations relation
    JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
    WHERE relation.task_id=$1
), visible_intents AS MATERIALIZED (
    SELECT intent.id, context.task_id, context.inherited
    FROM context
    JOIN exploration_nodes intent
      ON intent.exploration_id=context.exploration_id AND intent.kind='intent'
    WHERE (NOT context.inherited OR intent.state IN ('done','blocked','exhausted','stopped'))
      AND ($2 <= 0 OR intent.id < $2)
    ORDER BY intent.id DESC
    LIMIT NULLIF($3,0)
), anchored AS MATERIALIZED (
SELECT intent.id AS intent_id, asset.id AS asset_id, asset.type,
       CASE asset.type
         WHEN 'root_domain' THEN COALESCE(asset.domain,'')
         WHEN 'subdomain' THEN COALESCE(asset.domain,'')
         WHEN 'ip' THEN COALESCE(asset.ip,'')
         WHEN 'app' THEN COALESCE(asset.app_name,'')
         WHEN 'service' THEN COALESCE(NULLIF(asset.url,''), NULLIF(concat_ws(':', COALESCE(NULLIF(asset.domain,''), NULLIF(asset.ip,'')), asset.port::text),''), NULLIF(asset.service_name,''), '#' || asset.id::text)
         WHEN 'endpoint' THEN COALESCE(NULLIF(asset.url,''), '#' || asset.id::text)
         ELSE '#' || asset.id::text
       END AS label,
       COALESCE(link.source,'anchor') AS source,
       COALESCE(NULLIF(link.source_summary,''), '意图在黑板中锚定该资产') AS source_summary,
       link.source_node_id AS source_node_id, intent.task_id, intent.inherited
FROM visible_intents intent
JOIN exploration_anchors anchor ON anchor.node_id=intent.id
JOIN assets asset ON asset.id=anchor.asset_id
LEFT JOIN task_asset_links link ON link.task_id=intent.task_id AND link.asset_id=asset.id
), effective_assets AS MATERIALIZED (
    -- An asset can anchor hundreds of intents. Evaluate current-task effective
    -- authorization once per distinct asset instead of once per result row.
    SELECT candidate.asset_id
    FROM (SELECT DISTINCT asset_id FROM anchored) candidate
    WHERE task_asset_effectively_approved($1,candidate.asset_id)
), authorized AS MATERIALIZED (
    -- Provenance remains visible only when the owning local/source task also
    -- authorizes it. Evaluate that owner policy once per distinct task/asset.
    SELECT candidate.asset_id, candidate.task_id
    FROM (SELECT DISTINCT asset_id,task_id FROM anchored) candidate
    JOIN task_asset_links visible_link
      ON visible_link.task_id=candidate.task_id AND visible_link.asset_id=candidate.asset_id
    JOIN effective_assets effective ON effective.asset_id=candidate.asset_id
    WHERE task_asset_owner_approval_state(candidate.task_id,candidate.asset_id)='approved'
)
SELECT anchored.intent_id, anchored.asset_id, anchored.type, anchored.label,
       anchored.source, anchored.source_summary, anchored.source_node_id,
       anchored.task_id, anchored.inherited
FROM anchored JOIN authorized USING (asset_id,task_id)
ORDER BY anchored.inherited, anchored.intent_id DESC, anchored.asset_id`, taskID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IntentAsset{}
	for rows.Next() {
		var asset IntentAsset
		var sourceNodeID sql.NullInt64
		if err := rows.Scan(&asset.IntentID, &asset.AssetID, &asset.Type, &asset.Label,
			&asset.Source, &asset.SourceSummary, &sourceNodeID, &asset.SourceTaskID, &asset.Inherited); err != nil {
			return nil, err
		}
		if sourceNodeID.Valid {
			id := sourceNodeID.Int64
			asset.SourceNodeID = &id
		}
		out = append(out, asset)
	}
	return out, rows.Err()
}
