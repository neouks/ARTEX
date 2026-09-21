package traffic

import (
	"net/http"
	"net/url"
	"testing"
)

// newUpstreamTestTraffic builds a Traffic with only the fields chooseUpstream
// reads, so the priority table can be exercised without a database or a proxy.
func newUpstreamTestTraffic(t *testing.T, globalProxy string) *Traffic {
	t.Helper()
	tr := &Traffic{}
	if globalProxy != "" {
		u, err := ValidateProxyURL(globalProxy)
		if err != nil {
			t.Fatal(err)
		}
		tr.upstream.Store(u)
	}
	return tr
}

// reqFor builds a request as go-mitmproxy presents it to the decision callback,
// carrying the task identity the way authProxy injects it.
func reqFor(t *testing.T, taskID int64, host string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("GET", "http://"+host+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	WithTaskID(req, taskID)
	return req
}

// TestChooseUpstreamPriorityPinned locks the three-level priority. The middle
// level regressed once: `return nil, nil` after a failed smart check bypassed a
// configured global proxy entirely, while the UI still showed it as active.
func TestChooseUpstreamPriorityPinned(t *testing.T) {
	const global = "socks5://global:pw@global.example:1080"
	const pool = "socks5://pool:pw@pool.example:15818"

	withSmart := func(t *testing.T, enabled bool, marks map[string]bool) (*Traffic, string) {
		t.Helper()
		tr := newUpstreamTestTraffic(t, global)
		sp := NewSmartProxy()
		if err := sp.SetPool(pool); err != nil {
			t.Fatal(err)
		}
		sp.SetEnabled(enabled)
		for h := range marks {
			sp.Mark(7, h, "waf", "ai", "worker")
		}
		tr.SetSmartProxy(sp)
		return tr, pool
	}

	t.Run("smart OFF, global set, unmarked -> global", func(t *testing.T) {
		tr := newUpstreamTestTraffic(t, global)
		got, err := tr.chooseUpstream(reqFor(t, 7, "t.example"))
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Host != "global.example:1080" {
			t.Fatalf("got %v, want the global proxy", got)
		}
	})

	t.Run("smart ON, global set, UNMARKED -> global (the regression)", func(t *testing.T) {
		tr, _ := withSmart(t, true, nil)
		got, err := tr.chooseUpstream(reqFor(t, 7, "t.example"))
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatal("nil means the global proxy was bypassed; it must still apply")
		}
		if got.Host != "global.example:1080" {
			t.Fatalf("got %v, want the global proxy", got)
		}
	})

	t.Run("smart ON, marked -> pool wins over global", func(t *testing.T) {
		tr, poolHost := withSmart(t, true, map[string]bool{"t.example": true})
		got, err := tr.chooseUpstream(reqFor(t, 7, "t.example"))
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatal("marked host must route to the pool")
		}
		want, _ := url.Parse(poolHost)
		if got.Host != want.Host {
			t.Fatalf("got %v, want the pool %v", got, want)
		}
	})

	t.Run("smart ON, marked only for ANOTHER task -> global", func(t *testing.T) {
		tr, _ := withSmart(t, true, map[string]bool{"t.example": true})
		// Task 8 has no marks, so its traffic must not use the pool.
		got, err := tr.chooseUpstream(reqFor(t, 8, "t.example"))
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Host != "global.example:1080" {
			t.Fatalf("got %v, want the global proxy (per-task isolation)", got)
		}
	})

	t.Run("smart ON, marked, NO pool configured -> global", func(t *testing.T) {
		tr := newUpstreamTestTraffic(t, global)
		sp := NewSmartProxy() // pool never set
		sp.SetEnabled(true)
		sp.Mark(7, "t.example", "waf", "ai", "worker")
		tr.SetSmartProxy(sp)
		got, err := tr.chooseUpstream(reqFor(t, 7, "t.example"))
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Host != "global.example:1080" {
			t.Fatalf("got %v, want the global proxy when no pool exists", got)
		}
	})

	t.Run("smart OFF, no global -> direct", func(t *testing.T) {
		tr := newUpstreamTestTraffic(t, "")
		got, err := tr.chooseUpstream(reqFor(t, 7, "t.example"))
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil (direct)", got)
		}
	})

	t.Run("smart ON, unmarked, no global -> direct", func(t *testing.T) {
		tr, _ := withSmart(t, true, nil)
		tr.upstream.Store(nil)
		got, err := tr.chooseUpstream(reqFor(t, 7, "t.example"))
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil (direct)", got)
		}
	})

	t.Run("untagged traffic is never proxied by the smart feature", func(t *testing.T) {
		tr, _ := withSmart(t, true, map[string]bool{"t.example": true})
		// No task identity: mark lookup cannot match, so fall back to global.
		req, _ := http.NewRequest("GET", "http://t.example/", nil)
		got, err := tr.chooseUpstream(req)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Host != "global.example:1080" {
			t.Fatalf("got %v, want the global proxy for untagged traffic", got)
		}
	})
}
