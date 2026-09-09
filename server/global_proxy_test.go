package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCIPCC(t *testing.T) {
	got, err := parseCIPCC("\ufeffIP : 203.0.113.10\n地址 : 中国 广东省 广州市\n运营商 : 示例运营商\nURL : https://cip.cc/\n")
	if err != nil {
		t.Fatalf("parseCIPCC: %v", err)
	}
	if got.IP != "203.0.113.10" || got.Location != "中国 广东省 广州市" || got.ISP != "示例运营商" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestParseCIPCCFullWidthColonAndIPv6(t *testing.T) {
	got, err := parseCIPCC("IP：2001:db8::1\n地址：日本 东京都\n")
	if err != nil {
		t.Fatalf("parseCIPCC: %v", err)
	}
	if got.IP != "2001:db8::1" || got.Location != "日本 东京都" || got.ISP != "" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestParseCIPCCRejectsInvalidOrIncompleteResponse(t *testing.T) {
	for _, body := range []string{
		"IP : not-an-ip\n地址 : somewhere\n",
		"IP : 203.0.113.10\n",
		"地址 : somewhere\n",
	} {
		if _, err := parseCIPCC(body); err == nil {
			t.Fatalf("parseCIPCC(%q) expected error", body)
		}
	}
}

func TestProbeGlobalProxyURLUsesExplicitHTTPProxy(t *testing.T) {
	var gotProxyRequest bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProxyRequest = true
		if !strings.HasPrefix(r.RequestURI, "http://cip.test/") {
			t.Errorf("proxy received request URI %q, want absolute URI", r.RequestURI)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("IP : 203.0.113.10\n地址 : 测试地区\n运营商 : 测试运营商\n"))
	}))
	defer proxy.Close()

	got, err := probeGlobalProxyURL(context.Background(), proxy.URL, "http://cip.test/")
	if err != nil {
		t.Fatalf("probeGlobalProxyURL: %v", err)
	}
	if !gotProxyRequest {
		t.Fatal("probe did not use the explicit proxy")
	}
	if got.IP != "203.0.113.10" || got.Location != "测试地区" {
		t.Fatalf("unexpected result: %+v", got)
	}
}
