package traffic

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/Autumn-27/artex/guard"
	"testing"
	"time"
)

// TestSmartProxyTaggedRoutingIsDecisive proves the whole chain end to end with a
// REAL tagged request: identity parsed by authProxy -> injected into the request
// context -> recovered by the upstream callback -> routed to the pool.
//
// This is the test that would have caught a wrong context-injection form: if the
// in-place assignment were written as a rebinding, the pool would never be hit.
func TestSmartProxyTaggedRoutingIsDecisive(t *testing.T) {
	var poolHits, targetHits atomic.Int64

	// Stand-in egress proxy.
	poolLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer poolLn.Close()
	go (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		poolHits.Add(1)
		io.WriteString(w, "via-pool")
	})}).Serve(poolLn)

	// Target.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	go (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		io.WriteString(w, "target-direct")
	})}).Serve(targetLn)

	port := freePort(t)
	tr, err := Open(t.TempDir(), "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	sp := NewSmartProxy()
	if err := sp.SetPool("http://" + poolLn.Addr().String()); err != nil {
		t.Fatal(err)
	}
	sp.SetEnabled(true)
	tr.SetSmartProxy(sp)
	go func() { _ = tr.Start() }()
	addr := "127.0.0.1:" + port
	waitReachable(t, addr)

	targetURL := "http://" + targetLn.Addr().String() + "/"
	// The client sends the SAME signed credential ARTEX builds in TaskProxyAddr,
	// so authProxy's signature check accepts it as task-tagged traffic.
	proxyFor := func(taskID int64) *url.URL {
		user, pass, ok := guard.TaskProxyCredentials(taskID)
		if !ok {
			t.Fatalf("credentials for task %d unavailable", taskID)
		}
		u, err := url.Parse("http://" + user + ":" + pass + "@" + addr)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	proxyURL := proxyFor(7)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
		},
		Timeout: 5 * time.Second,
	}

	// --- Phase 1: not marked -> must reach the target directly -------------
	resp, err := client.Get(targetURL)
	if err != nil {
		t.Fatalf("phase1: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := string(b); got != "target-direct" {
		t.Fatalf("phase1: want direct, got %q", got)
	}

	// --- Phase 2: mark it -> tagged traffic must go through the pool -------
	sp.Mark(7, mustHost(t, targetLn.Addr().String()), "test WAF", "ai", "worker-1")

	resp2, err := client.Get(targetURL)
	if err != nil {
		t.Fatalf("phase2: %v", err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if got := string(b2); got != "via-pool" {
		t.Fatalf("phase2: marked host must route to the pool, got %q", got)
	}
	if poolHits.Load() == 0 {
		t.Fatal("phase2: pool was never contacted — context injection failed")
	}

	// --- Phase 3: another task must still go direct ------------------------
	otherClient := &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyFor(8)),
			DisableKeepAlives: true,
		},
		Timeout: 5 * time.Second,
	}
	before := targetHits.Load()
	resp3, err := otherClient.Get(targetURL)
	if err != nil {
		t.Fatalf("phase3: %v", err)
	}
	b3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if got := string(b3); got != "target-direct" {
		t.Fatalf("phase3: task 8 must stay direct, got %q", got)
	}
	if targetHits.Load() <= before {
		t.Fatal("phase3: task 8 did not reach the target directly")
	}

	t.Logf("verified: pool hits=%d target hits=%d", poolHits.Load(), targetHits.Load())
}
