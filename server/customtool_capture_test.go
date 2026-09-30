package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/tool"
)

func TestHTTPToolPassesCaptureContext(t *testing.T) {
	body := strings.Repeat("中文 fixture\n", 4000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201); w.Write([]byte(body)) }))
	defer srv.Close()
	execRaw, _ := json.Marshal(httpExec{URL: srv.URL})
	s := &Server{}
	configured := s.buildCustomTool(&db.Tool{Key: "fixture_http", Kind: "http", Exec: execRaw})
	dir := t.TempDir()
	tc := &tool.ToolContext{OutputDir: dir, MaxOutputChars: 100}
	res, err := configured.Call(context.Background(), json.RawMessage(`{}`), tc)
	if err != nil || res.IsError || !strings.Contains(res.Flatten(), "<persisted-output>") {
		t.Fatalf("missing context capture: %+v err=%v", res, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("spill files=%d err=%v", len(files), err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Body != body || decoded.Status != 201 {
		t.Fatal("lost HTTP status or full response", err)
	}
	res, err = s.runHTTPTool(context.Background(), execRaw, nil, nil)
	if err != nil || len(res.Flatten()) >= len(body) || !strings.Contains(res.Flatten(), "characters truncated]") {
		t.Fatal("default capture limit was not used", err)
	}
}

func TestScriptToolCapturesFullOutput(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if detectPython() == "" {
		t.Skip("python interpreter unavailable")
	}
	dir := t.TempDir()
	s := &Server{m: &Manager{pg: pg, dir: dir}}
	spec, _ := json.Marshal(scriptExec{Code: `print("中文 fixture" * 5000)`})
	res, err := s.runScriptTool(context.Background(), "capture-fixture", spec, nil, &tool.ToolContext{WorkingDir: dir, OutputDir: dir, MaxOutputChars: 100})
	if err != nil || res.IsError || !strings.Contains(res.Flatten(), "<persisted-output>") {
		t.Fatalf("script capture failed: %+v err=%v", res, err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "output-*.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("script spills=%d err=%v", len(files), err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || string(raw) != strings.Repeat("中文 fixture", 5000)+"\n" {
		t.Fatal("full script output lost", err)
	}
}
