package mcphttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/tool"
)

func TestRemoteToolCapturePreservesErrorAndFullOutput(t *testing.T) {
	for _, failed := range []bool{false, true} {
		body := strings.Repeat("中文 response\n", 4000)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request rpcRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
				"content": []map[string]string{{"type": "text", "text": body}}, "isError": failed,
			}})
		}))
		client := &Client{server: "fixture", url: srv.URL, http: srv.Client()}
		target := client.wrap(remoteTool{Name: "long_output"})
		dir := t.TempDir()
		tc := &tool.ToolContext{OutputDir: dir, MaxOutputChars: 100}
		res, err := target.Call(context.Background(), json.RawMessage(`{}`), tc)
		if err != nil || res.IsError != failed || len(res.Flatten()) >= len(body) || !strings.Contains(res.Flatten(), "<persisted-output>") {
			t.Fatalf("capture failed: error=%v result=%+v", err, res)
		}
		files, err := os.ReadDir(dir)
		if err != nil || len(files) != 1 {
			t.Fatalf("spill files=%d err=%v", len(files), err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
		if err != nil || string(raw) != body {
			t.Fatal("full remote output lost", err)
		}
		if tool.CaptureOnce(tc, res.Flatten()) != res.Flatten() {
			t.Fatal("remote output captured twice")
		}
		res, err = target.Call(context.Background(), json.RawMessage(`{}`), nil)
		if err != nil || len(res.Flatten()) >= len(body) || !strings.Contains(res.Flatten(), "characters truncated]") {
			t.Fatal("nil context did not apply default output cap", err)
		}
		srv.Close()
	}
}
