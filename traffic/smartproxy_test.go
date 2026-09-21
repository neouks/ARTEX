package traffic

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestSmartProxyDisabledIsNoOp pins the red-line guarantee: when the feature is
// off, the decision never routes anywhere, so existing installs are unaffected.
func TestSmartProxyDisabledIsNoOp(t *testing.T) {
	s := NewSmartProxy()
	if err := s.SetPool("http://pool.example:8080"); err != nil {
		t.Fatal(err)
	}
	s.Mark(7, "target.example", "test", "ai", "worker-1")

	if s.Enabled() {
		t.Fatal("a fresh SmartProxy must be disabled")
	}
	if u, ok := s.ShouldProxy(7, "target.example"); ok || u != nil {
		t.Fatalf("disabled proxy must not route: u=%v ok=%v", u, ok)
	}
}

// TestSmartProxyTaskScope is the core isolation guarantee: a mark recorded by one
// task must not affect any other task, so a new task always starts direct.
func TestSmartProxyTaskScope(t *testing.T) {
	s := NewSmartProxy()
	s.SetEnabled(true)
	if err := s.SetPool("http://pool.example:8080"); err != nil {
		t.Fatal(err)
	}
	s.Mark(7, "target.example", "Cloudflare 403", "ai", "worker-1")

	if _, ok := s.ShouldProxy(7, "target.example"); !ok {
		t.Fatal("owning task should be routed to the pool")
	}
	if u, ok := s.ShouldProxy(8, "target.example"); ok || u != nil {
		t.Fatalf("other task must stay direct: u=%v ok=%v", u, ok)
	}
	// A brand-new task has an empty list by construction.
	if s.Marked(99, "target.example") {
		t.Fatal("unrelated task must have no marks")
	}
}

// TestSmartProxyHostNormalization covers case/whitespace tolerance, so a mark and
// a lookup that differ only in case still match.
func TestSmartProxyHostNormalization(t *testing.T) {
	s := NewSmartProxy()
	s.SetEnabled(true)
	_ = s.SetPool("http://pool.example:8080")
	s.Mark(1, "  Target.Example  ", "reason", "ai", "worker")

	if !s.Marked(1, "target.example") {
		t.Fatal("mark must be normalized to lowercase and trimmed")
	}
	if _, ok := s.ShouldProxy(1, "TARGET.EXAMPLE"); !ok {
		t.Fatal("lookup must be case-insensitive")
	}
}

// TestSmartProxyUnmarkAndClear covers the two removal paths used by the UI and by
// task deletion.
func TestSmartProxyUnmarkAndClear(t *testing.T) {
	s := NewSmartProxy()
	s.SetEnabled(true)
	_ = s.SetPool("http://pool.example:8080")
	s.Mark(3, "a.example", "r", "ai", "w")
	s.Mark(3, "b.example", "r", "ai", "w")
	if len(s.List()) != 2 {
		t.Fatalf("expected 2 marks, got %d", len(s.List()))
	}

	s.Unmark(3, "a.example")
	if s.Marked(3, "a.example") {
		t.Fatal("unmark did not take effect")
	}
	if !s.Marked(3, "b.example") {
		t.Fatal("unmark removed the wrong host")
	}

	s.ClearTask(3)
	if len(s.List()) != 0 {
		t.Fatalf("ClearTask left %d marks", len(s.List()))
	}
}

// TestSmartProxyPoolClearedFallsBackDirect guards the case where the switch is on
// and a host is marked, but no pool is configured: we must not route to a nil
// proxy, which would break the request entirely.
func TestSmartProxyPoolClearedFallsBackDirect(t *testing.T) {
	s := NewSmartProxy()
	s.SetEnabled(true)
	s.Mark(5, "target.example", "r", "ai", "w")
	// No pool configured.
	if u, ok := s.ShouldProxy(5, "target.example"); ok || u != nil {
		t.Fatalf("no pool must mean direct: u=%v ok=%v", u, ok)
	}
}

// TestSmartProxyInvalidPoolRejected ensures a malformed pool URL is refused at
// configuration time rather than silently breaking every marked request.
func TestSmartProxyInvalidPoolRejected(t *testing.T) {
	s := NewSmartProxy()
	if err := s.SetPool("ftp://nope"); err == nil {
		t.Fatal("unsupported proxy scheme must be rejected")
	}
	if err := s.SetPool("pool-without-scheme:8080"); err == nil {
		t.Fatal("a pool URL without a scheme must be rejected")
	}
}

// TestTaskIDContextRoundTrip is the mechanism the whole feature depends on: the
// identity authProxy injects must be recoverable by the upstream callback.
// The in-place assignment form is required; see WithTaskID's doc comment.
func TestTaskIDContextRoundTrip(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://target.example/", nil)
	if got := TaskIDFrom(req); got != 0 {
		t.Fatalf("untagged request must report task 0, got %d", got)
	}

	WithTaskID(req, 42)

	if got := TaskIDFrom(req); got != 42 {
		t.Fatalf("task id did not survive: got %d want 42", got)
	}
	// The mutation must be visible through the original pointer, which is exactly
	// what the downstream callback relies on.
	if req.Context().Value(smartProxyCtxKey{}) == nil {
		t.Fatal("context value missing after in-place injection")
	}
}

// TestSmartProxyHitsCounted verifies the counter the settings panel surfaces.
func TestSmartProxyHitsCounted(t *testing.T) {
	s := NewSmartProxy()
	s.SetEnabled(true)
	_ = s.SetPool("http://pool.example:8080")
	s.Mark(1, "target.example", "r", "ai", "w")
	for i := 0; i < 3; i++ {
		if _, ok := s.ShouldProxy(1, "target.example"); !ok {
			t.Fatal("expected routing")
		}
	}
	if got := s.hits.Load(); got != 3 {
		t.Fatalf("hits = %d, want 3", got)
	}
}

// TestSmartProxyConcurrent ensures the hot path is safe under concurrent access,
// which matters because every proxied request evaluates the decision.
func TestSmartProxyConcurrent(t *testing.T) {
	s := NewSmartProxy()
	s.SetEnabled(true)
	_ = s.SetPool("http://pool.example:8080")
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			taskID := int64(i%2 + 1)
			for j := 0; j < 200; j++ {
				s.Mark(taskID, "target.example", "r", "ai", "w")
				s.ShouldProxy(taskID, "target.example")
				s.Marked(taskID, "target.example")
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if u, _ := url.Parse("http://pool.example:8080"); u == nil {
		t.Fatal("sanity")
	}
}

// TestValidateProxyURLRejectsMisconfigurations covers the two malformed shapes
// that produced a locally-rejected request (empty 502 in milliseconds) while
// looking like a blocked target: a URL without a port, and a provider's
// "get exit IP" API endpoint pasted where the proxy endpoint belongs.
func TestValidateProxyURLRejectsMisconfigurations(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"api endpoint instead of proxy", "https://tps.kdlapi.com/api/gettps/?secret_id=x&format=text", "API"},
		{"port missing", "socks5://user:pass@host", "端口"},
		{"no scheme", "host:15818", "协议"},
		{"unsupported scheme", "ftp://host:21", "不支持"},
	}
	for _, tc := range cases {
		_, err := ValidateProxyURL(tc.raw)
		if err == nil {
			t.Fatalf("%s: %q must be rejected", tc.name, tc.raw)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error should mention %q, got %v", tc.name, tc.want, err)
		}
	}
}

// TestValidateProxyURLAcceptsRealProxies pins the shapes that must keep working,
// including the exact value the user's working global proxy uses.
func TestValidateProxyURLAcceptsRealProxies(t *testing.T) {
	for _, raw := range []string{
		"socks5://t18990622300474:r7kmticd@q311.kdltps.com:15818",
		"http://user:pass@proxy.example:8080",
		"https://proxy.example:3128",
		"http://127.0.0.1:7890",
		"socks5://proxy.example:1080/", // a bare trailing slash is not a path
	} {
		if _, err := ValidateProxyURL(raw); err != nil {
			t.Fatalf("%q must be accepted, got %v", raw, err)
		}
	}
}

// TestSmartProxyOnChangeFires covers the defect that made marks vanish on
// restart: Mark/Unmark/ClearTask mutated memory only, so an agent's mark was
// never persisted. The hook must fire on every mutation, otherwise a marked
// host silently falls back to direct dialing after a restart.
func TestSmartProxyOnChangeFires(t *testing.T) {
	s := NewSmartProxy()
	s.SetEnabled(true)
	_ = s.SetPool("http://pool.example:8080")

	var snapshots [][]SmartProxyHost
	s.SetOnChange(func(hosts []SmartProxyHost) { snapshots = append(snapshots, hosts) })

	s.Mark(1, "a.example", "waf", "ai", "worker")
	if len(snapshots) != 1 || len(snapshots[0]) != 1 {
		t.Fatalf("Mark must fire onChange with the new list, got %d snapshots", len(snapshots))
	}
	if snapshots[0][0].Host != "a.example" || snapshots[0][0].TaskID != 1 {
		t.Fatalf("snapshot content wrong: %+v", snapshots[0][0])
	}

	s.Unmark(1, "a.example")
	if len(snapshots) != 2 || len(snapshots[1]) != 0 {
		t.Fatalf("Unmark must fire onChange with the emptied list, got %d", len(snapshots))
	}

	s.Mark(2, "b.example", "waf", "ai", "worker")
	s.ClearTask(2)
	if len(snapshots) != 4 || len(snapshots[3]) != 0 {
		t.Fatalf("ClearTask must fire onChange, got %d snapshots", len(snapshots))
	}

	// ClearTask on an unknown task must NOT fire a redundant write.
	before := len(snapshots)
	s.ClearTask(999)
	if len(snapshots) != before {
		t.Fatal("ClearTask on an unknown task must not fire onChange")
	}
}

// TestSmartProxyBareIPAndIPv6 covers the second question raised: do bare IPs and
// IPv6 literals work? Both are supported, but the IPv6 bracketed form had to be
// normalized or a mark silently failed to match the unbracketed lookup the
// decision layer produces (reproduced experimentally before the fix).
func TestSmartProxyBareIPAndIPv6(t *testing.T) {
	newSP := func() *SmartProxy {
		sp := NewSmartProxy()
		sp.SetEnabled(true)
		if err := sp.SetPool("socks5://u:p@127.0.0.1:1080"); err != nil {
			t.Fatal(err)
		}
		return sp
	}

	// Bare IPv4: marked as written, and a version WITH a port must not match,
	// because the decision layer always strips the port first.
	sp := newSP()
	sp.Mark(1, "1.2.3.4", "waf", "ai", "w")
	if _, ok := sp.ShouldProxy(1, "1.2.3.4"); !ok {
		t.Fatal("bare IPv4 must route")
	}
	if _, ok := sp.ShouldProxy(1, "1.2.3.4:80"); ok {
		t.Fatal("a port-suffixed lookup must not match a port-less mark")
	}

	// IPv6: bracketed and bare forms must be interchangeable in both directions.
	sp.Mark(1, "[2001:db8::1]", "waf", "ai", "w")
	if _, ok := sp.ShouldProxy(1, "2001:db8::1"); !ok {
		t.Fatal("bracketed mark must match the unbracketed decision form")
	}
	sp2 := newSP()
	sp2.Mark(1, "2001:db8::1", "waf", "ai", "w")
	if _, ok := sp2.ShouldProxy(1, "[2001:db8::1]"); !ok {
		t.Fatal("unbracketed mark must match a bracketed lookup")
	}

	// Unmark must use the same normalization, otherwise a mark cannot be removed
	// through the other spelling.
	sp.Unmark(1, "2001:db8::1")
	if _, ok := sp.ShouldProxy(1, "[2001:db8::1]"); ok {
		t.Fatal("unmark via the alternate spelling must take effect")
	}
}

// TestSmartProxyUnmarkIsIdempotent covers the redundant-write defect: a delete of
// something that was never marked (UI retry, double task delete) must not fire
// the persistence hook, otherwise the store is rewritten for no reason.
func TestSmartProxyUnmarkIsIdempotent(t *testing.T) {
	s := NewSmartProxy()
	s.SetEnabled(true)
	_ = s.SetPool("http://pool.example:8080")
	var writes int
	s.SetOnChange(func([]SmartProxyHost) { writes++ })

	// Nothing marked yet: deleting must be a no-op.
	s.Unmark(1, "absent.example")
	if writes != 0 {
		t.Fatalf("unmark of an absent host wrote %d times, want 0", writes)
	}

	s.Mark(1, "a.example", "r", "ai", "w")
	if writes != 1 {
		t.Fatalf("mark wrote %d times, want 1", writes)
	}
	s.Unmark(1, "a.example")
	if writes != 2 {
		t.Fatalf("real unmark wrote %d times in total, want 2", writes)
	}
	// Second delete of the same host must not write again.
	s.Unmark(1, "a.example")
	if writes != 2 {
		t.Fatalf("repeat unmark wrote %d times in total, want 2", writes)
	}
}

// TestSmartProxyListIsOrdered covers the presentation defect: sync.Map iteration
// order is unspecified, so the list (and the persisted JSON) reshuffled between
// calls. Ordering must be stable and deterministic.
func TestSmartProxyListIsOrdered(t *testing.T) {
	s := NewSmartProxy()
	s.SetEnabled(true)
	_ = s.SetPool("http://pool.example:8080")
	for _, h := range []string{"z.example", "a.example", "m.example"} {
		s.Mark(7, h, "r", "ai", "w")
	}
	s.Mark(3, "b.example", "r", "ai", "w")

	wantTask := []int64{3, 7, 7, 7}
	wantHost := []string{"b.example", "a.example", "m.example", "z.example"}

	// Repeat to show the order does not depend on map internals.
	for attempt := 0; attempt < 5; attempt++ {
		got := s.List()
		if len(got) != 4 {
			t.Fatalf("len=%d want 4", len(got))
		}
		for i := range got {
			if got[i].TaskID != wantTask[i] || got[i].Host != wantHost[i] {
				t.Fatalf("attempt %d: index %d = (%d,%s), want (%d,%s)",
					attempt, i, got[i].TaskID, got[i].Host, wantTask[i], wantHost[i])
			}
		}
	}
}
