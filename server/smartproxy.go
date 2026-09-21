package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/traffic"
)

// Settings keys for the smart proxy (per-request proxy selection).
const (
	settingSmartProxy      = "smart_proxy"
	settingSmartProxyPool  = "smart_proxy_pool"
	settingSmartProxyHosts = "smart_proxy_hosts"
)

// SmartProxyEnabled reports whether per-request proxy selection is on.
func (m *Manager) SmartProxyEnabled() bool {
	if m.smart == nil {
		return false
	}
	return m.smart.Enabled()
}

// SetSmartProxy toggles the feature and persists it. Takes effect immediately:
// the routing decision runs per request, so no agent rebuild is needed.
func (m *Manager) SetSmartProxy(on bool) error {
	if err := m.pg.SetBool(settingSmartProxy, on); err != nil {
		return err
	}
	if m.smart != nil {
		m.smart.SetEnabled(on)
	}
	return nil
}

// SmartProxyPool returns the egress proxy used for marked hosts ("" = direct).
func (m *Manager) SmartProxyPool() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.smartProxyPool
}

// SetSmartProxyPool validates, persists and applies the pool proxy used for
// marked hosts. Empty clears it, sending marked hosts back to direct dialing.
func (m *Manager) SetSmartProxyPool(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		if _, err := traffic.ValidateProxyURL(raw); err != nil {
			return err
		}
	}
	if err := m.pg.SetSetting(settingSmartProxyPool, raw); err != nil {
		return err
	}
	m.mu.Lock()
	m.smartProxyPool = raw
	m.mu.Unlock()
	if m.smart != nil {
		return m.smart.SetPool(raw)
	}
	return nil
}

// SmartProxyHosts lists every marked host for the settings panel.
func (m *Manager) SmartProxyHosts() []traffic.SmartProxyHost {
	if m.smart == nil {
		return []traffic.SmartProxyHost{}
	}
	return m.smart.List()
}

// UnmarkSmartProxyHost removes one (task, host) mark and persists the result.
func (m *Manager) UnmarkSmartProxyHost(taskID int64, host string) error {
	if m.smart == nil {
		return nil
	}
	m.smart.Unmark(taskID, host)
	raw, err := json.Marshal(m.smart.List())
	if err != nil {
		return err
	}
	return m.pg.SetSetting(settingSmartProxyHosts, string(raw))
}

// persistSmartProxyHosts snapshots the current marks into settings so they
// survive a restart without needing a dedicated table.
func (m *Manager) persistSmartProxyHosts() error {
	if m.smart == nil {
		return nil
	}
	raw, err := json.Marshal(m.smart.List())
	if err != nil {
		return err
	}
	return m.pg.SetSetting(settingSmartProxyHosts, string(raw))
}

// deleteSmartProxyHost removes one mark, addressed by task_id + host query args.
func (s *Server) deleteSmartProxyHost(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if taskID == "" || host == "" {
		writeErr(w, 400, "task_id 与 host 不能为空")
		return
	}
	id, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "task_id 必须为正整数")
		return
	}
	if err := s.m.UnmarkSmartProxyHost(id, host); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// smartProxyHostView is the JSON shape the settings page renders.
type smartProxyHostView struct {
	Host     string `json:"host"`
	Reason   string `json:"reason"`
	Source   string `json:"source"`
	TaskID   int64  `json:"task_id"`
	AgentKey string `json:"agent_key"`
	AddedAt  string `json:"added_at"`
}

// smartProxyHostViews converts marks for the API response.
func smartProxyHostViews(hosts []traffic.SmartProxyHost) []smartProxyHostView {
	out := make([]smartProxyHostView, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, smartProxyHostView{
			Host:     h.Host,
			Reason:   h.Reason,
			Source:   h.Source,
			TaskID:   h.TaskID,
			AgentKey: h.AgentKey,
			AddedAt:  h.AddedAt.Format(time.RFC3339),
		})
	}
	return out
}

// SmartProxyHostsForTask lists the marks owned by one task, for the task detail
// page. Task-scoped listing is what makes the per-task model visible in the UI.
func (m *Manager) SmartProxyHostsForTask(taskID int64) []traffic.SmartProxyHost {
	if m.smart == nil {
		return []traffic.SmartProxyHost{}
	}
	out := []traffic.SmartProxyHost{}
	for _, h := range m.smart.List() {
		if h.TaskID == taskID {
			out = append(out, h)
		}
	}
	return out
}

// ClearSmartProxyTask drops every mark owned by one task and persists the result.
// Called when a task is deleted so marks never outlive their task.
func (m *Manager) ClearSmartProxyTask(taskID int64) error {
	if m.smart == nil {
		return nil
	}
	m.smart.ClearTask(taskID)
	return m.persistSmartProxyHosts()
}

// getTaskSmartProxyHosts returns the marks owned by the task in the path.
func (s *Server) getTaskSmartProxyHosts(w http.ResponseWriter, r *http.Request) {
	id, _ := pathInt(r, "id")
	if id <= 0 {
		writeErr(w, 400, "无效的任务 ID")
		return
	}
	writeJSON(w, 200, map[string]any{"hosts": smartProxyHostViews(s.m.SmartProxyHostsForTask(int64(id)))})
}

// deleteTaskSmartProxyHost removes one mark owned by the task in the path.
func (s *Server) deleteTaskSmartProxyHost(w http.ResponseWriter, r *http.Request) {
	id, _ := pathInt(r, "id")
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if id <= 0 || host == "" {
		writeErr(w, 400, "任务 ID 与 host 不能为空")
		return
	}
	if err := s.m.UnmarkSmartProxyHost(int64(id), host); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// clearSmartProxyMarks drops every mark owned by a genuinely deleted task.
//
// Kept separate from forgetTask because forgetTask also runs on ARCHIVE, and an
// archived task may be restored — clearing there would lose marks permanently.
func (m *Manager) clearSmartProxyMarks(taskID int64) {
	if m.smart == nil || taskID <= 0 {
		return
	}
	m.smart.ClearTask(taskID) // fires the onChange hook, which persists
}
