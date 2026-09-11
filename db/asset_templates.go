package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// DNSRecordHost separates non-host TXT/SRV owner labels from the real host.
func DNSRecordHost(owner, recordType string) (string, bool, error) {
	owner = DomainKey(owner)
	recordType = strings.ToUpper(strings.TrimSpace(recordType))
	if !strings.HasPrefix(owner, "_") || (recordType != "TXT" && recordType != "SRV") {
		return owner, false, nil
	}
	if len(owner) > 253 {
		return "", true, fmt.Errorf("DNS owner too long")
	}
	labels := strings.Split(owner, ".")
	i := 0
	for i < len(labels) && strings.HasPrefix(labels[i], "_") {
		if len(labels[i]) > 63 || len(labels[i]) < 2 {
			return "", true, fmt.Errorf("invalid DNS owner")
		}
		for _, c := range labels[i] {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
				return "", true, fmt.Errorf("invalid DNS owner")
			}
		}
		i++
	}
	host, err := NormalizeAgentHost(strings.Join(labels[i:], "."), false)
	return host, true, err
}

func (s *AssetStore) registerDNSMetadata(req UpsertSubdomainReq, host string) (int64, error) {
	return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
		root, isApex := RootDomain(host)
		var id int64
		var err error
		if isApex && root == host {
			id, err = scoped.UpsertRootDomain(UpsertRootDomainReq{
				Domain: host, ICP: req.ICP, TaskID: req.TaskID, AgentDiscovered: req.AgentDiscovered,
			})
		} else {
			base := req
			base.Domain = host
			base.RecordType = ""
			base.RecordValue = nil
			id, err = scoped.UpsertSubdomain(base)
		}
		if err != nil {
			return 0, err
		}
		record, err := json.Marshal(map[string]any{"owner": DomainKey(req.Domain), "type": req.RecordType, "values": req.RecordValue})
		if err != nil {
			return 0, err
		}
		_, err = scoped.tx.Exec(`UPDATE assets SET extra=jsonb_set(extra,'{dns_records}',
   (SELECT jsonb_agg(DISTINCT v) FROM jsonb_array_elements(COALESCE(extra->'dns_records','[]'::jsonb)||jsonb_build_array($2::jsonb)) v)) WHERE id=$1`, id, string(record))
		return id, err
	})
}

func ValidAssetApprovalTemplate(v string) bool {
	return v == "all_assets" || v == "related_assets" || v == "explicit_targets"
}

func (s *AssetStore) TaskApprovalTemplate(taskID int64) (string, error) {
	var template string
	err := s.queryRow(`SELECT asset_approval_template FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&template)
	return template, err
}

// RegisterAgentAsset commits discovery, provenance and template authorization
// together. All side-effect hosts share this transaction via the scoped store.
func (s *AssetStore) RegisterAgentAsset(taskID int64, agentKey string, ownerNode int64, write func(*AssetStore) (int64, error)) (int64, error) {
	return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
		if err := scoped.enableAgentDiscoveryMode(); err != nil {
			return 0, err
		}
		id, err := write(scoped)
		if err != nil || taskID <= 0 {
			return id, err
		}
		if _, err := scoped.tx.Exec(`DELETE FROM task_asset_blocks b USING assets a
WHERE b.task_id=$1 AND a.id=$2 AND b.asset_type NOT IN ('root_domain','subdomain','ip')
AND b.block_kind<>'invalid' AND (b.asset_id=a.id OR b.asset_key=task_asset_identity_key(a))`, taskID, id); err != nil {
			return 0, err
		}
		var node *int64
		summary := "Agent 通过 insert_assets 登记"
		if ownerNode > 0 {
			node = &ownerNode
			summary = fmt.Sprintf("Worker 意图 #%d 通过 insert_assets 登记", ownerNode)
		}
		// Deleted host identities must not be reattached by discovery. Active
		// manual blocks retain their links and test history.
		if _, err := scoped.tx.Exec(`UPDATE assets a SET task_ids=array_remove(a.task_ids,$1)
WHERE $1=ANY(a.task_ids) AND EXISTS(SELECT 1 FROM task_asset_blocks b
WHERE b.task_id=$1 AND b.block_kind='deleted' AND b.asset_type IN ('root_domain','subdomain','ip')
AND task_asset_host_within(task_asset_host(a),b.host_key))`, taskID); err != nil {
			return 0, err
		}
		var linked bool
		if err := scoped.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_asset_links WHERE task_id=$1 AND asset_id=$2)`, taskID, id).Scan(&linked); err != nil {
			return 0, err
		}
		if linked {
			if err := scoped.SetTaskAssetSource(taskID, id, "agent", summary, node); err != nil {
				return 0, err
			}
		}
		return id, nil
	})
}

// RegisterUserAsset is the transactional UI/API path, never an Agent tool.
func (s *AssetStore) RegisterUserAsset(taskID int64, write func(*AssetStore) (int64, error)) (int64, error) {
	return s.RegisterUserAssetWithSource(taskID, "manual", "用户手动添加", write)
}

// RegisterUserAssetWithSource keeps the concrete operator entry point for audit
// while applying the same atomic approval and parent-host registration rules.
func (s *AssetStore) RegisterUserAssetWithSource(taskID int64, source, evidence string, write func(*AssetStore) (int64, error)) (int64, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	switch source {
	case "manual", "api", "task", "direct", "company":
	default:
		return 0, fmt.Errorf("%w: invalid user asset source %q", ErrTaskAssetInvalid, source)
	}
	return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
		if _, err := scoped.tx.Exec(`SELECT set_config('artex.user_asset_registration','on',true)`); err != nil {
			return 0, err
		}
		if taskID > 0 {
			var exists bool
			if err := scoped.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, taskID).Scan(&exists); err != nil {
				return 0, err
			}
			if !exists {
				return 0, ErrTaskAssetTaskNotFound
			}
		}
		id, err := write(scoped)
		if err != nil || taskID <= 0 {
			return id, err
		}
		if err := authorizeUserAsset(scoped, taskID, id, source, evidence, true); err != nil {
			return 0, err
		}
		return id, seedUserAssetGrants(scoped.tx, taskID)
	})
}

func authorizeUserAsset(s *AssetStore, taskID, id int64, source, evidence string, restore bool) error {
	if restore {
		if _, err := s.tx.Exec(`DELETE FROM task_asset_blocks b USING assets a WHERE a.id=$2 AND b.task_id=$1 AND b.block_kind<>'invalid' AND (b.asset_id=a.id OR b.asset_key=task_asset_identity_key(a))`, taskID, id); err != nil {
			return err
		}
	}
	_, err := s.tx.Exec(`UPDATE task_asset_links SET source=$3,source_summary=$4,
 approval_state='approved',approved_by='user',approved_at=now(),approval_reason=$4
 WHERE task_id=$1 AND asset_id=$2 AND NOT task_asset_blocked($1,$2)
 AND ($5 OR (approval_state NOT IN ('revoked','blocked') AND source NOT IN ('manual','direct','company','api','task')))`, taskID, id, source, evidence, restore)
	if err != nil {
		return err
	}
	var typ, host string
	if err := s.tx.QueryRow(`SELECT type,task_asset_host(assets) FROM assets WHERE id=$1`, id).Scan(&typ, &host); err != nil {
		return err
	}
	if (typ == "service" || typ == "endpoint") && host != "" {
		var parent int64
		err = s.tx.QueryRow(`SELECT a.id FROM assets a JOIN task_asset_links l ON l.asset_id=a.id WHERE l.task_id=$1 AND a.type IN ('root_domain','subdomain','ip') AND task_asset_host(a)=$2 ORDER BY a.id LIMIT 1`, taskID, host).Scan(&parent)
		if err == nil {
			return authorizeUserAsset(s, taskID, parent, source, evidence, false)
		}
		if err != sql.ErrNoRows {
			return err
		}
		if net.ParseIP(host) != nil {
			parent, err = s.UpsertIP(UpsertIPReq{IP: host, TaskID: taskID})
		} else {
			parent, err = s.UpsertSubdomain(UpsertSubdomainReq{Domain: host, TaskID: taskID})
		}
		if err != nil {
			return err
		}
		return authorizeUserAsset(s, taskID, parent, source, evidence, false)
	}
	return nil
}

// NormalizeAgentHost validates a host, not arbitrary DNS owner names or text.
func NormalizeAgentHost(raw string, allowSingle bool) (string, error) {
	h := DomainKey(raw)
	if ip := net.ParseIP(h); ip != nil {
		return ip.String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(h)
	if err != nil {
		return "", fmt.Errorf("invalid host %q", raw)
	}
	h = strings.ToLower(ascii)
	if h == "" || len(h) > 253 || (!allowSingle && !strings.Contains(h, ".")) {
		return "", fmt.Errorf("invalid host %q: 需要明确的主机名，不能使用变量或占位符", raw)
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid host %q", raw)
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return "", fmt.Errorf("invalid host %q: 不得使用变量、下划线或占位符", raw)
			}
		}
	}
	return h, nil
}

func (s *AssetStore) agentHost(raw string, taskID int64) (string, error) {
	h, err := NormalizeAgentHost(raw, false)
	if err == nil {
		return h, nil
	}
	h, singleErr := NormalizeAgentHost(raw, true)
	if singleErr != nil || taskID <= 0 {
		return "", err
	}
	var explicit bool
	q := `SELECT EXISTS(SELECT 1 FROM task_asset_grants WHERE task_id=$1 AND kind='host' AND value=$2)`
	if s.tx != nil {
		singleErr = s.tx.QueryRow(q, taskID, h).Scan(&explicit)
	} else {
		singleErr = s.db.QueryRow(q, taskID, h).Scan(&explicit)
	}
	if singleErr == nil && explicit {
		return h, nil
	}
	return "", err
}

func (s *AssetStore) agentURL(raw string, taskID int64) (string, error) {
	u, err := url.Parse(normalizeURL(raw))
	if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", fmt.Errorf("invalid asset URL %q", raw)
	}
	h, err := s.agentHost(u.Hostname(), taskID)
	if err != nil {
		return "", err
	}
	if p := u.Port(); p != "" {
		h = net.JoinHostPort(h, p)
	} else if net.ParseIP(h) != nil && strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	u.Host = h
	return u.String(), nil
}

func seedUserAssetGrants(tx *sql.Tx, taskID int64) error {
	rows, err := tx.Query(`SELECT DISTINCT COALESCE(a.domain,''),COALESCE(a.ip,''),COALESCE(a.url,'')
 FROM task_asset_links l JOIN assets a ON a.id=l.asset_id WHERE l.task_id=$1 AND l.source IN ('manual','direct','company','api','task')`, taskID)
	if err != nil {
		return err
	}
	type seed struct {
		domain, ip, url string
	}
	var seeds []seed
	for rows.Next() {
		var x seed
		if err := rows.Scan(&x.domain, &x.ip, &x.url); err != nil {
			rows.Close()
			return err
		}
		seeds = append(seeds, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	hosts, roots := []string{}, []string{}
	seen := make(map[string]bool, len(seeds))
	for _, x := range seeds {
		h := x.domain
		if h == "" && x.url != "" {
			if u, e := url.Parse(x.url); e == nil {
				h = u.Hostname()
			}
		}
		if h == "" {
			h = x.ip
		}
		h, err = NormalizeAgentHost(h, true)
		if err != nil {
			continue
		}
		if seen[h] {
			continue
		}
		seen[h] = true
		root := ""
		if net.ParseIP(h) == nil {
			root, _ = RootDomain(h)
		}
		hosts = append(hosts, h)
		roots = append(roots, root)
	}
	if len(hosts) > 0 {
		if _, err := tx.Exec(`INSERT INTO task_asset_grants(task_id,kind,value,root_domain,source)
SELECT $1,'host',host,root,'user' FROM unnest($2::text[],$3::text[]) AS seed(host,root)
ON CONFLICT DO NOTHING`, taskID, hosts, roots); err != nil {
			return err
		}
	}
	return reconcileAssetTemplate(tx, taskID)
}

func reconcileAssetTemplate(tx *sql.Tx, taskID int64) error {
	_, err := tx.Exec(`UPDATE task_asset_links l SET approval_state='pending' FROM assets a
 WHERE l.task_id=$1 AND a.id=l.asset_id AND l.approval_state='pending'
 AND NOT task_asset_blocked($1,a.id) AND task_asset_template_allows($1,a)`, taskID)
	return err
}

func (d *DB) SetAssetApprovalTemplate(taskID int64, value string) error {
	if !ValidAssetApprovalTemplate(value) {
		return fmt.Errorf("%w: invalid asset_approval_template", ErrTaskAssetInvalid)
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockCompanyScopeMutation(tx); err != nil {
		return err
	}
	var started bool
	if err = tx.QueryRow(`SELECT first_run_at IS NOT NULL FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, taskID).Scan(&started); err != nil {
		return err
	}
	if started {
		return fmt.Errorf("%w: 任务已启动，不能修改审批模板", ErrTaskAssetInvalid)
	}
	if _, err = tx.Exec(`UPDATE tasks SET asset_approval_template=$2 WHERE id=$1`, taskID, value); err != nil {
		return err
	}
	// Re-evaluate only template decisions, never operator approvals or revocations.
	if _, err = tx.Exec(`UPDATE task_asset_links SET approval_state='pending',approved_by='',approved_at=NULL WHERE task_id=$1 AND approval_state='approved' AND approved_by='template'`, taskID); err != nil {
		return err
	}
	if err = reconcileAssetTemplate(tx, taskID); err != nil {
		return err
	}
	return tx.Commit()
}
