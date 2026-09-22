package server

import (
	"context"
	"encoding/json"
	actool "github.com/Autumn-27/norma/tool"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellDetectionDoesNotExecute(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tool bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "tool with spaces")
	marker := filepath.Join(dir, "ran")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	command, e := shellDetectionCommand(bin, "", actool.ShellProfile{})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]any{"command": command})
	r, e := actool.NewBash().Call(t.Context(), raw, nil)
	if e != nil || r.IsError || !strings.Contains(r.Flatten(), bin) {
		t.Fatal(r, e)
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("probe executed the tool")
	}
	// Configured directory wins over PATH without executing the candidate.
	local := filepath.Join(dir, "sh")
	if e := os.WriteFile(local, []byte("#!/bin/sh\nexit 99\n"), 0700); e != nil {
		t.Fatal(e)
	}
	command, e = shellDetectionCommand("sh", dir, actool.ShellProfile{})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(map[string]any{"command": command})
	r, e = actool.NewBash().Call(t.Context(), raw, nil)
	if e != nil || r.IsError || !strings.Contains(r.Flatten(), local) {
		t.Fatal(r, e)
	}
	os.Chmod(bin, 0600)
	command, _ = shellDetectionCommand(bin, "", actool.ShellProfile{})
	raw, _ = json.Marshal(map[string]any{"command": command})
	r, _ = actool.NewBash().Call(t.Context(), raw, nil)
	if !r.IsError {
		t.Fatal("non-executable accepted")
	}
	if _, e := shellDetectionCommand("nmap --version", "", actool.ShellProfile{}); e == nil {
		t.Fatal("accepted arguments")
	}
}
func TestShellTestRunAndCancellation(t *testing.T) {
	s, _ := modeServer(t)
	var before int
	if e := s.m.pg.QueryRow(`SELECT count(*) FROM tool_usage`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	for _, tt := range []struct {
		req      testToolReq
		failed   bool
		contains string
	}{
		{testToolReq{Action: "check", Executable: "sh"}, false, "sh"},
		{testToolReq{Action: "check", Executable: "artex_missing_tool_123"}, true, "未找到"},
		{testToolReq{Action: "run", Command: "printf hello"}, false, "hello"},
		{testToolReq{Action: "run", Command: "printf failed >&2; exit 1"}, true, "failed"},
	} {
		tt.req.Kind = "shell"
		body, _ := json.Marshal(tt.req)
		w := httptest.NewRecorder()
		s.pgTestCustomTool(w, httptest.NewRequest("POST", "/api/tools/custom/test", strings.NewReader(string(body))))
		var got struct {
			Output string `json:"output"`
			Failed bool   `json:"is_error"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != 200 || got.Failed != tt.failed || !strings.Contains(got.Output, tt.contains) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// The old command request remains supported, and neither probe type is metered.
	w := httptest.NewRecorder()
	s.pgTestCustomTool(w, httptest.NewRequest("POST", "/api/tools/custom/test", strings.NewReader(`{"kind":"command","exec":{"command":"printf legacy"},"params":{}}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "legacy") {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	s.testShellTool(w, httptest.NewRequest("POST", "/", nil), testToolReq{Action: "run", Command: "printf '%040000d' 0"})
	if w.Code != 200 || w.Body.Len() > 32000 {
		t.Fatal("output not bounded", w.Code, w.Body.Len())
	}
	var after int
	s.m.pg.QueryRow(`SELECT count(*) FROM tool_usage`).Scan(&after)
	if before != after {
		t.Fatal("editor tests changed usage", before, after)
	}
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		if cancelled {
			cancel()
		}
		defer cancel()
		w := httptest.NewRecorder()
		started := time.Now()
		s.testShellTool(w, httptest.NewRequest("POST", "/", nil).WithContext(ctx), testToolReq{Action: "run", Command: "sleep 10"})
		if time.Since(started) > 3*time.Second || !strings.Contains(w.Body.String(), `"is_error":true`) {
			t.Fatal(w.Body.String())
		}
	}
}
