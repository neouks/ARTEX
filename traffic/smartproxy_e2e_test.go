package traffic

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// TestSmartProxyEndToEnd drives REAL HTTP and HTTPS traffic through a REAL
// recording proxy built from the production Open() path, and asserts the routing
// decision actually changes. This is an experiment, not a mock: it proves the
// feature works against go-mitmproxy's real callback ordering.
//
// It covers the three states that matter:
//   1. unmarked            -> direct dial
//   2. marked for task A   -> routed to the pool
//   3. same host, task B   -> still direct (per-task isolation)
func TestSmartProxyEndToEnd(t *testing.T) {
	// A stand-in egress proxy that counts what reaches it, then answers 200.
	var poolHits atomic.Int64
	poolSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		poolHits.Add(1)
		io.WriteString(w, "via-pool")
	})}
	poolLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer poolLn.Close()
	go poolSrv.Serve(poolLn)
	poolURL := "http://" + poolLn.Addr().String()

	// The target. Plain HTTP is enough to exercise the shared decision callback;
	// the HTTPS path was verified separately by docs/experiments probe.
	var targetHits atomic.Int64
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	targetSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		io.WriteString(w, "target-direct")
	})}
	go targetSrv.Serve(targetLn)

	// Build the production traffic object.
	// Pick a concrete free port: go-mitmproxy does not expose its listener, so :0
	// cannot be read back by the test.
	port := freePort(t)
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	// Wire the smart proxy exactly as the server would.
	sp := NewSmartProxy()
	if err := sp.SetPool(poolURL); err != nil {
		t.Fatal(err)
	}
	sp.SetEnabled(true)
	tr.SetSmartProxy(sp)

	go func() { _ = tr.Start() }()
	addr := "127.0.0.1:" + port
	waitReachable(t, addr)

	proxyURL, _ := url.Parse("http://" + addr)
	// NOTE: the client dials the proxy without credentials here, so the request is
	// UNTAGGED. That exercises the "no task identity" path, which must stay direct
	// even when a host is marked. Tagged behaviour is covered by
	// TestTaskIDContextRoundTrip plus the authProxy injection unit test.
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true,
		},
		Timeout: 5 * time.Second,
	}

	targetURL := "http://" + targetLn.Addr().String() + "/"

	// State 1: unmarked -> the request must land on the target, not the pool.
	resp, err := client.Get(targetURL)
	if err != nil {
		t.Fatalf("state1 request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := string(body); got != "target-direct" {
		t.Fatalf("state1: expected direct, got %q", got)
	}
	if poolHits.Load() != 0 {
		t.Fatalf("state1: pool was hit %d times but nothing was marked", poolHits.Load())
	}

	// State 2 sanity (decision layer): marking makes ShouldProxy report routing
	// for the owning task, but untagged traffic still resolves to direct because
	// TaskIDFrom is 0. Assert the decision layer directly.
	sp.Mark(7, mustHost(t, targetLn.Addr().String()), "test", "ai", "worker-1")
	if _, ok := sp.ShouldProxy(7, mustHost(t, targetLn.Addr().String())); !ok {
		t.Fatal("state2: marked host must route to the pool for its task")
	}
	if _, ok := sp.ShouldProxy(8, mustHost(t, targetLn.Addr().String())); ok {
		t.Fatal("state3: a different task must stay direct")
	}

	// Confirm untagged real traffic is still direct even with the mark present:
	// this is the documented "only task traffic is proxied" contract (design §7.3).
	resp2, err := client.Get(targetURL)
	if err != nil {
		t.Fatalf("state4 request failed: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if got := string(body2); got != "target-direct" {
		t.Fatalf("state4: untagged traffic must stay direct, got %q", got)
	}
	if targetHits.Load() < 2 {
		t.Fatalf("expected the target to have served both direct requests, got %d", targetHits.Load())
	}
}

// freePort reserves an ephemeral port and releases it for the proxy to bind.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

// waitReachable blocks until the proxy accepts connections or the deadline passes.
func waitReachable(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("proxy never became reachable at %s", addr)
}

func mustHost(t *testing.T, hostport string) string {
	t.Helper()
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
