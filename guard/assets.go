package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// TaskAssetPolicy preflights explicit asset IDs, structured targets and URLs.
// It does not infer network destinations from arbitrary shell arguments;
// actual requests must also be authorized at the network boundary.
type TaskAssetPolicy struct {
	Store  *db.AssetStore
	TaskID int64
	Scope  string
}

// Intent identity survives worker slot reuse and transcript compaction.
func AssetSkipScope(ctx context.Context) string {
	ri := llmrec.RunInfoFrom(ctx)
	if ri.IntentID > 0 {
		return fmt.Sprintf("worker:%d", ri.IntentID)
	}
	if ri.AgentKey == "planner" {
		return "planner"
	}
	return "mainagent"
}

// AssetPolicyHooks wraps an existing hook runner and blocks pending, revoked or
// tombstoned asset targets before the underlying tool is executed.
func AssetPolicyHooks(inner harness.HookRunner, store *db.AssetStore, taskID int64) harness.HookRunner {
	if store == nil || taskID <= 0 {
		return inner
	}
	return assetPolicyHooks{inner: inner, policy: TaskAssetPolicy{Store: store, TaskID: taskID}}
}

// AssetPolicyHooksWithGuard keeps task authorization and the ordinary Guard in
// one hook chain. Authorization denials are recorded in the same audit stream
// as intercept-rule denials, while allowed calls continue through Guard.
func AssetPolicyHooksWithGuard(g *Guard, store *db.AssetStore, taskID int64) harness.HookRunner {
	var inner harness.HookRunner
	if g != nil {
		inner = g.Hooks()
	}
	if store == nil || taskID <= 0 {
		return inner
	}
	return assetPolicyHooks{
		inner:  inner,
		policy: TaskAssetPolicy{Store: store, TaskID: taskID},
		audit:  g,
	}
}

type assetPolicyHooks struct {
	inner  harness.HookRunner
	policy TaskAssetPolicy
	audit  *Guard
}

func (h assetPolicyHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	h.policy.Scope = AssetSkipScope(ctx)
	if reason, audit := h.policy.check(name, input); reason != "" {
		if h.audit != nil && audit {
			h.audit.record(name, "block", reason, assetPolicyAuditSubject(name, input))
		}
		return true, reason, nil
	}
	if h.inner != nil {
		return h.inner.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}
func (h assetPolicyHooks) PostToolUse(ctx context.Context, name string, input, result []byte, isErr bool) {
	if h.inner != nil {
		h.inner.PostToolUse(ctx, name, input, result, isErr)
	}
}
func (h assetPolicyHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	if h.inner != nil {
		return h.inner.Stop(ctx, messages)
	}
	return false, nil, ""
}

func assetPolicyAuditSubject(name string, input []byte) string {
	var value struct {
		Command string `json:"command"`
		Text    string `json:"text"`
	}
	if json.Unmarshal(input, &value) != nil {
		return ""
	}
	switch name {
	case "Bash", "shell_open":
		return value.Command
	case "shell_send":
		return value.Text
	default:
		return ""
	}
}

var urlPattern = regexp.MustCompile(`(?i)(?:https?|wss?)://[^\s"'<>]+`)

func (p TaskAssetPolicy) Check(tool string, input []byte) string {
	reason, _ := p.check(tool, input)
	return reason
}

func (p TaskAssetPolicy) denial(err error, hosts []string, ids []int64) (string, bool) {
	reason := fmt.Sprintf("任务资产执行被阻止：%v", err)
	rows, saveErr := p.Store.RememberTaskAssetDenials(p.TaskID, hosts, ids, p.Scope)
	if saveErr != nil {
		log.Printf("[asset-skip] task %d 记录失败: %v", p.TaskID, saveErr)
		return reason + "。跳过本项资源，继续其他已授权测试，不要重试。", true
	}
	if len(rows) == 0 {
		return reason, true
	}
	first := false
	for _, row := range rows {
		if row.Attempts == 1 {
			first = true
		}
	}
	return reason + "。" + db.TaskAssetSkipMessage(rows), first
}

func (p TaskAssetPolicy) check(tool string, input []byte) (string, bool) {
	if p.Store == nil || p.TaskID <= 0 {
		return "", false
	}
	// Discovery registers candidates; authorization belongs to the transaction
	// and the returned executable view, not a pre-tool test of its new hosts.
	if tool == "insert_assets" || tool == "register_user_target" || tool == "list_task_assets" {
		return "", false
	}
	var value any
	if json.Unmarshal(input, &value) == nil {
		ids := collectAssetIDs(value)
		if len(ids) > 0 {
			if err := p.Store.ValidateTaskAssetsApproved(p.TaskID, ids); err != nil {
				return p.denial(err, nil, ids)
			}
		}
	}
	// Evidence and summaries may mention unrelated hosts without targeting them.
	// These write tools enforce authorization on their explicit/implicit IDs.
	if tool == "record_fact" || tool == "report_finding" {
		return "", false
	}
	hosts := collectHosts(string(input))
	if err := p.Store.ValidateTaskHostsApproved(p.TaskID, hosts); err != nil {
		return p.denial(err, hosts, nil)
	}
	return "", false
}

func collectHosts(text string) []string {
	seen := make(map[string]bool)
	hosts := make([]string, 0)
	add := func(host string) {
		host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
		if zone := strings.LastIndex(host, "%"); zone > 0 {
			host = host[:zone]
		}
		if ip := net.ParseIP(host); ip != nil {
			host = ip.String()
		}
		if host != "" && !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	var structured any
	if json.Unmarshal([]byte(text), &structured) == nil {
		collectStructuredHosts(structured, add)
	}
	// Positive identification only: structured targets and explicit URL hosts.
	// Never interpret bare shell arguments as hosts or maintain exclusions for
	// file/header/cookie options. Opaque commands require network-layer policy;
	// this preflight is not a shell interpreter or an egress sandbox.
	for _, rawURL := range urlPattern.FindAllString(text, -1) {
		rawURL = strings.TrimRight(rawURL, `.,;:!?)]}`)
		if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
			add(u.Hostname())
		}
	}
	return hosts
}

func collectStructuredHosts(value any, add func(string)) {
	var walk func(any, bool)
	walk = func(current any, hostField bool) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
				isHostField := normalized == "url" || normalized == "uri" || normalized == "host" ||
					normalized == "hostname" || normalized == "domain" || normalized == "ip" || normalized == "ipv4" || normalized == "ipv6" ||
					normalized == "address" || normalized == "target" || normalized == "endpoint"
				walk(child, isHostField)
			}
		case []any:
			for _, child := range typed {
				walk(child, hostField)
			}
		case string:
			if !hostField {
				return
			}
			candidate := strings.Trim(strings.TrimSpace(typed), `"'`)
			if candidate == "" {
				return
			}
			if ip := net.ParseIP(strings.Trim(candidate, "[]")); ip != nil {
				add(ip.String())
				return
			}
			if strings.Contains(candidate, "://") {
				if parsed, err := url.Parse(candidate); err == nil && parsed.Hostname() != "" {
					add(parsed.Hostname())
				}
				return
			}
			if parsed, err := url.Parse("//" + candidate); err == nil && parsed.Hostname() != "" {
				candidate = parsed.Hostname()
			}
			if normalized, err := db.NormalizeAgentHost(candidate, true); err == nil {
				add(normalized)
			}
		}
	}
	walk(value, false)
}

func collectAssetIDs(value any) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				normalizedKey := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(k))
				if strings.HasSuffix(normalizedKey, "assetids") || strings.HasSuffix(normalizedKey, "assetid") {
					collectIDValue(child, seen, &out)
				} else {
					walk(child)
				}
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(value)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func collectIDValue(v any, seen map[int64]bool, out *[]int64) {
	switch x := v.(type) {
	case float64:
		if x > 0 && x == float64(int64(x)) {
			id := int64(x)
			if !seen[id] {
				seen[id] = true
				*out = append(*out, id)
			}
		}
	case json.Number:
		if id, err := x.Int64(); err == nil && id > 0 && !seen[id] {
			seen[id] = true
			*out = append(*out, id)
		}
	case []any:
		for _, child := range x {
			collectIDValue(child, seen, out)
		}
	}
}
