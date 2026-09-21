package traffic

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// smartProxyCtxKey carries the task identity from authProxy (which parses and
// consumes Proxy-Authorization) down to the upstream-selection callback, which
// runs later and can no longer read that header.
type smartProxyCtxKey struct{}

// SmartProxyHost is one marked host: traffic to it leaves via the proxy pool
// instead of dialing directly, until the entry is removed.
type SmartProxyHost struct {
	Host     string    `json:"host"`
	Reason   string    `json:"reason"`
	Source   string    `json:"source"`    // "ai" | "manual"
	TaskID   int64     `json:"task_id"`   // owning task; used for display and cleanup
	AgentKey string    `json:"agent_key"` // which agent marked it
	AddedAt  time.Time `json:"added_at"`
	Hits     int64     `json:"hits"`
}

// SmartProxy decides, per request, whether traffic to a host should go through
// the proxy pool. Scope is per-task: an entry recorded under task A does not
// affect task B, so every new task starts from a clean direct-dial state.
type SmartProxy struct {
	enabled atomic.Bool
	// hosts is taskID -> hostname -> *SmartProxyHost. sync.Map keeps the hot
	// path lock-free for the common (nothing marked) case.
	hosts sync.Map
	// pool is the egress proxy used for marked hosts.
	pool atomic.Pointer[url.URL]
	// hits counts decisions routed to the pool, for the settings UI.
	hits atomic.Int64
	// onChange, when set, is called after any mutation so the caller can persist
	// the list. Without it, marks live only in memory and are lost on restart —
	// which silently reverted marked hosts to direct dialing.
	onChange func([]SmartProxyHost)
}

// NewSmartProxy builds an empty, disabled resolver.
func NewSmartProxy() *SmartProxy { return &SmartProxy{} }

// SetOnChange registers a hook invoked after every mutation with a snapshot of
// the full list. The server uses it to persist marks, so a mark made by an agent
// survives a restart.
func (s *SmartProxy) SetOnChange(fn func([]SmartProxyHost)) { s.onChange = fn }

// notifyChange fires the persistence hook, if one is registered.
func (s *SmartProxy) notifyChange() {
	if s.onChange != nil {
		s.onChange(s.List())
	}
}

// SetEnabled toggles the feature. Disabled restores the legacy single-upstream
// behaviour, making this a zero-risk rollout switch.
func (s *SmartProxy) SetEnabled(on bool) { s.enabled.Store(on) }

// Enabled reports whether the feature is on.
func (s *SmartProxy) Enabled() bool { return s.enabled.Load() }

// SetPool points marked-host traffic at the given egress proxy. An empty string
// clears it, which sends marked hosts back to direct dialing.
func (s *SmartProxy) SetPool(raw string) error {
	if strings.TrimSpace(raw) == "" {
		s.pool.Store(nil)
		return nil
	}
	u, err := ValidateProxyURL(raw)
	if err != nil {
		return err
	}
	s.pool.Store(u)
	return nil
}

// normalizeHostKey canonicalizes a host for list lookups.
//
// Two shapes must collapse to one key or a mark silently never matches:
//   - case and surrounding whitespace (DNS names)
//   - bracketed IPv6 ("[2001:db8::1]") vs the bare form the decision layer
//     produces after hostOnly strips the port ("2001:db8::1")
//
// Experimentally reproduced: a bracketed mark did not match the unbracketed
// lookup, so the host kept direct-dialing despite being marked.
func normalizeHostKey(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	// Drop the brackets an IPv6 literal carries in a URL or CONNECT line.
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return host
}

// Mark records host under taskID. Idempotent: re-marking refreshes the reason
// instead of duplicating the entry.
//
// Concurrency: the entry is replaced with a NEW struct rather than mutated in
// place. sync.Map only guards the map slot, not the pointed-to struct, so
// in-place field writes race with a concurrent List()/Marked() reader (caught by
// TestSmartProxyConcurrent under -race).
func (s *SmartProxy) Mark(taskID int64, host, reason, source, agentKey string) {
	host = normalizeHostKey(host)
	if host == "" || taskID <= 0 {
		return
	}
	prev, _ := s.hosts.LoadOrStore(taskID, &sync.Map{})
	m := prev.(*sync.Map)
	taskMap, _ := m.LoadOrStore(host, &SmartProxyHost{})
	e := taskMap.(*SmartProxyHost)
	m.Store(host, &SmartProxyHost{ // fresh struct: no shared mutable state
		Host:     host,
		Reason:   reason,
		Source:   source,
		TaskID:   taskID,
		AgentKey: agentKey,
		AddedAt:  time.Now(),
		Hits:     e.Hits,
	})
	s.notifyChange()
}

// Unmark removes one host from one task.
func (s *SmartProxy) Unmark(taskID int64, host string) {
	taskMap, ok := s.hosts.Load(taskID)
	if !ok {
		return
	}
	// Only persist when something was actually removed: an idempotent delete
	// (e.g. the UI retrying, or the task deleted twice) must not write the store.
	if _, loaded := taskMap.(*sync.Map).LoadAndDelete(normalizeHostKey(host)); !loaded {
		return
	}
	s.notifyChange()
}

// Marked reports whether host is marked for this task.
func (s *SmartProxy) Marked(taskID int64, host string) bool {
	taskMap, ok := s.hosts.Load(taskID)
	if !ok {
		return false
	}
	_, ok = taskMap.(*sync.Map).Load(normalizeHostKey(host))
	return ok
}

// List returns every marked host, for the settings panel.
func (s *SmartProxy) List() []SmartProxyHost {
	out := []SmartProxyHost{}
	s.hosts.Range(func(_, v any) bool {
		v.(*sync.Map).Range(func(_, hv any) bool {
			out = append(out, *hv.(*SmartProxyHost))
			return true
		})
		return true
	})
	// sync.Map iteration order is unspecified, so without sorting the list (and
	// therefore the persisted JSON and the settings UI) reshuffles on every call.
	sort.Slice(out, func(i, j int) bool {
		if out[i].TaskID != out[j].TaskID {
			return out[i].TaskID < out[j].TaskID
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// ClearTask drops every mark belonging to one task, so marks never outlive the
// task that created them.
func (s *SmartProxy) ClearTask(taskID int64) {
	if _, ok := s.hosts.LoadAndDelete(taskID); ok {
		s.notifyChange()
	}
}

// ShouldProxy is the per-request decision invoked by the upstream callback. It
// returns the pool URL when this task has marked this host, and ok=false for
// direct dialing.
func (s *SmartProxy) ShouldProxy(taskID int64, host string) (*url.URL, bool) {
	if !s.enabled.Load() || taskID <= 0 {
		return nil, false
	}
	if !s.Marked(taskID, host) {
		return nil, false
	}
	u := s.pool.Load()
	if u == nil {
		return nil, false
	}
	s.hits.Add(1)
	return u, true
}

// WithTaskID returns a request whose context carries the task identity, so the
// later upstream callback can recover it.
//
// NOTE (verified experimentally): the assignment MUST be in-place via
// *req = ..., because authProxy receives a *http.Request and rebinding the local
// variable does not propagate downstream. Measured 3/3 OK in-place, 0/3 rebound.
func WithTaskID(req *http.Request, taskID int64) {
	*req = *req.WithContext(context.WithValue(req.Context(), smartProxyCtxKey{}, taskID))
}

// TaskIDFrom recovers the identity injected by WithTaskID. Zero means untagged
// traffic (independent chat, enrichment, UI), which is never proxied by this
// feature.
func TaskIDFrom(req *http.Request) int64 {
	v, _ := req.Context().Value(smartProxyCtxKey{}).(int64)
	return v
}
