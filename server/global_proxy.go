package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/traffic"
)

const cipCCURL = "https://cip.cc"

type cipCCResult struct {
	IP        string
	Location  string
	ISP       string
	LatencyMs int64
}

// parseCIPCC parses the line-oriented response returned by cip.cc. The service
// currently uses "key : value" lines; accepting both ASCII and full-width
// colons keeps the parser tolerant of formatting changes.
func parseCIPCC(body string) (cipCCResult, error) {
	var out cipCCResult
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(body, "\ufeff")))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		key, value, ok := splitCIPCCLine(line)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "ip":
			fields := strings.Fields(value)
			if len(fields) > 0 {
				out.IP = fields[0]
			}
		case "地址", "location":
			out.Location = strings.TrimSpace(value)
		case "运营商", "isp":
			out.ISP = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return cipCCResult{}, fmt.Errorf("读取 cip.cc 响应失败: %w", err)
	}
	parsed := net.ParseIP(out.IP)
	if parsed == nil {
		return cipCCResult{}, fmt.Errorf("cip.cc 返回的 IP 无效")
	}
	if out.Location == "" {
		return cipCCResult{}, fmt.Errorf("cip.cc 响应缺少地理位置")
	}
	out.IP = parsed.String()
	return out, nil
}

func splitCIPCCLine(line string) (key, value string, ok bool) {
	idx := strings.IndexByte(line, ':')
	sepLen := 1
	if fullWidth := strings.IndexRune(line, '：'); fullWidth >= 0 && (idx < 0 || fullWidth < idx) {
		idx = fullWidth
		sepLen = len(string('：'))
	}
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+sepLen:])
	return key, value, key != ""
}

func probeGlobalProxy(ctx context.Context, proxy string) (cipCCResult, error) {
	return probeGlobalProxyURL(ctx, proxy, cipCCURL)
}

func probeGlobalProxyURL(ctx context.Context, proxy, endpoint string) (cipCCResult, error) {
	proxy = strings.TrimSpace(proxy)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// An empty proxy means a deliberate direct test. Do not inherit HTTP_PROXY or
	// HTTPS_PROXY from the server environment in that case.
	transport.Proxy = nil
	if proxy != "" {
		u, err := traffic.ValidateProxyURL(proxy)
		if err != nil {
			return cipCCResult{}, err
		}
		transport.Proxy = http.ProxyURL(u)
	}

	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return cipCCResult{}, fmt.Errorf("构建 cip.cc 请求失败: %w", err)
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "ARTEX/global-proxy-probe")

	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return cipCCResult{}, fmt.Errorf("通过代理请求 cip.cc 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return cipCCResult{}, fmt.Errorf("cip.cc 返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return cipCCResult{}, fmt.Errorf("读取 cip.cc 响应失败: %w", err)
	}
	out, err := parseCIPCC(string(body))
	if err != nil {
		return cipCCResult{}, err
	}
	out.LatencyMs = time.Since(started).Milliseconds()
	return out, nil
}

func (s *Server) testGlobalProxy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Proxy string `json:"proxy"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := probeGlobalProxy(r.Context(), req.Proxy)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"ip":         result.IP,
		"location":   result.Location,
		"isp":        result.ISP,
		"latency_ms": result.LatencyMs,
	})
}
