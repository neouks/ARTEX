package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// SmartProxyView is the narrow surface this package needs: mark a host, inspect
// a mark. Declaring it here (instead of importing the traffic package) keeps the
// dependency direction one-way — traffic never imports agent — so there is no
// import cycle, and tests can supply a stub.
type SmartProxyView interface {
	Mark(taskID int64, host, reason, source, agentKey string)
	Marked(taskID int64, host string) bool
}

// smartProxyRule tells the agent when switching a host to the proxy pool is the
// right move — and, just as importantly, when it is not. Without this guidance
// the tool is either never used or used on every 4xx.
const smartProxyRule = "\n\n【访问受阻时的处理】若目标站点的正常访问被 WAF/风控阻断（403 且带 WAF 特征、429 限速、连接被重置或超时、JS 挑战页），可把该主机标记为经代理出口访问：先用 check_host_proxy 查看是否已标记，未标记则调用 mark_host_proxy 并写明判定依据。注意区分：403 带 WWW-Authenticate、401、以及返回正常页面结构的业务性 403 都不算被拦截，不要标记。已标记后仍失败不要反复重标，记录事实并继续其他方向。标记仅对当前任务生效。host 参数填主机名或 IP，不带协议、路径和端口；IPv6 不要加方括号。"

// globalSmartProxy is the process-wide selector, set once at server startup.
//
// Rationale: the two tools below are registered on every agent surface, but a
// ToolSet is constructed in several places. Wiring the selector into each
// construction site proved error-prone — a missed site silently broke the tools
// ("智能代理未启用") for a whole agent kind. A package-level default removes that
// class of bug: any ToolSet that was not wired explicitly still works.
var globalSmartProxy SmartProxyView

// SetGlobalSmartProxy installs the process-wide selector. Called once by the
// server after the manager is built.
func SetGlobalSmartProxy(s SmartProxyView) { globalSmartProxy = s }

// SetSmartProxy installs a selector for this ToolSet only. Nil falls back to the
// process-wide default, so callers never have to wire every surface.
func (t *ToolSet) SetSmartProxy(s SmartProxyView) { t.smart = s }

// smartProxy resolves the selector for this ToolSet: an explicit per-set value
// wins, otherwise the process-wide default.
func (t *ToolSet) smartProxy() SmartProxyView {
	if t.smart != nil {
		return t.smart
	}
	return globalSmartProxy
}

// markHostProxy lets the agent declare that a host must be reached through the
// proxy pool. The mark is scoped to the current task.
func (t *ToolSet) markHostProxy() actool.CoreTool {
	return writeTool("mark_host_proxy",
		"把某主机标记为经代理池访问（仅当前任务生效）。当该主机因 WAF/风控拦截导致正常访问受阻时使用。先判断是否确为拦截（WAF 特征/429/连接重置/JS 挑战），并用 check_host_proxy 确认尚未标记，再调用本工具并写明拦截依据。",
		obj(map[string]any{
			"host":   str("被拦截的主机名或 IP：不含协议、路径和端口（域名填 example.com；IPv4 填 1.2.3.4；IPv6 填 2001:db8::1，不要加方括号）"),
			"reason": str("判定为拦截的依据，例如「Cloudflare 403 挑战页」"),
		}, "host", "reason"),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			sp := t.smartProxy()
			if sp == nil {
				return actool.Errorf("智能代理未启用，无法标记主机"), nil
			}
			var in struct{ Host, Reason string }
			if err := json.Unmarshal(raw, &in); err != nil {
				return actool.Errorf("参数无效：" + err.Error()), nil
			}
			if in.Host == "" || in.Reason == "" {
				return actool.Errorf("host 与 reason 不能为空"), nil
			}
			ri := RunInfoFrom(ctx)
			if ri.TaskID <= 0 {
				return actool.Errorf("缺少任务上下文，无法标记主机"), nil
			}
			sp.Mark(ri.TaskID, in.Host, in.Reason, "ai", ri.AgentKey)
			return jsonResult(map[string]any{
				"host":    in.Host,
				"marked":  true,
				"scope":   "task",
				"task_id": ri.TaskID,
			})
		})
}

// checkHostProxy lets the agent avoid re-marking a host it already marked.
func (t *ToolSet) checkHostProxy() actool.CoreTool {
	return readTool("check_host_proxy",
		"查询某主机当前是否已被标记为经代理池访问（仅当前任务范围）。在 mark_host_proxy 之前调用，避免重复标记。",
		obj(map[string]any{"host": str("主机名或 IP（不含端口；IPv6 不加方括号）")}, "host"),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			sp := t.smartProxy()
			if sp == nil {
				return actool.Errorf("智能代理未启用"), nil
			}
			var in struct{ Host string }
			if err := json.Unmarshal(raw, &in); err != nil {
				return actool.Errorf("参数无效：" + err.Error()), nil
			}
			if in.Host == "" {
				return actool.Errorf("host 不能为空"), nil
			}
			ri := RunInfoFrom(ctx)
			return jsonResult(map[string]any{
				"host":   in.Host,
				"marked": sp.Marked(ri.TaskID, in.Host),
				"scope":  "task",
			})
		})
}
