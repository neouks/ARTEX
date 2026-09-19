package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

type runtimeMCPClient struct {
	id     int
	closed bool
}

func (c *runtimeMCPClient) Close() error { c.closed = true; return nil }
func (c *runtimeMCPClient) Tools(context.Context) ([]actool.CoreTool, error) {
	return []actool.CoreTool{actool.Build(actool.Spec{Name: "mcp__test__echo", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		if c.closed {
			return actool.Errorf("closed"), nil
		}
		return actool.Text(fmt.Sprint(c.id)), nil
	}})}, nil
}
func TestMCPRuntimeCacheIsolationAndInvalidation(t *testing.T) {
	cache := &mcpRuntimeCache{}
	m := &db.MCPServer{ID: 1, Name: "test", Transport: "http", URL: "https://example.test"}
	calls := 0
	dial := func(ctx context.Context, _ *db.MCPServer) (mcpClient, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 16*time.Second {
			t.Fatal("no discovery timeout")
		}
		calls++
		return &runtimeMCPClient{id: calls}, nil
	}
	first, closeFirst, err := cache.prepare(context.Background(), m, dial)
	if err != nil {
		t.Fatal(err)
	}
	second, closeSecond, err := cache.prepare(context.Background(), m, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSecond()
	if calls != 1 {
		t.Fatal("cache hit opened transport")
	}
	closeFirst()
	res, _ := second[0].Call(context.Background(), json.RawMessage(`{}`), nil)
	if res.Flatten() != "2" {
		t.Fatalf("shared run transport: %+v", res)
	}
	res, _ = first[0].Call(context.Background(), json.RawMessage(`{}`), nil)
	if !res.IsError {
		t.Fatal("closed run reopened")
	}
	m.Env = json.RawMessage(`{"Authorization":"changed"}`)
	_, closeThird, err := cache.prepare(context.Background(), m, dial)
	if err != nil {
		t.Fatal(err)
	}
	closeThird()
	if calls != 3 {
		t.Fatal("configuration change not invalidated")
	}
	cache.mu.Lock()
	entry := cache.entries[m.ID]
	entry.until = time.Now().Add(-time.Second)
	cache.entries[m.ID] = entry
	cache.mu.Unlock()
	_, closeFourth, err := cache.prepare(context.Background(), m, dial)
	if err != nil {
		t.Fatal(err)
	}
	closeFourth()
	if calls != 4 {
		t.Fatal("expired metadata reused")
	}
}
func TestMCPRuntimeFailureNotCached(t *testing.T) {
	cache := &mcpRuntimeCache{}
	m := &db.MCPServer{ID: 1}
	_, _, err := cache.prepare(context.Background(), m, func(context.Context, *db.MCPServer) (mcpClient, error) { return nil, fmt.Errorf("offline") })
	if err == nil || len(cache.entries) != 0 {
		t.Fatal("failure cached as empty tools")
	}
}

func TestMCPRuntimeConcurrentLazyCalls(t *testing.T) {
	cache := &mcpRuntimeCache{}
	m := &db.MCPServer{ID: 1}
	calls := 0
	dial := func(context.Context, *db.MCPServer) (mcpClient, error) {
		calls++
		return &runtimeMCPClient{id: calls}, nil
	}
	_, closeWarm, err := cache.prepare(context.Background(), m, dial)
	if err != nil {
		t.Fatal(err)
	}
	closeWarm()
	ts, closeRun, err := cache.prepare(context.Background(), m, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRun()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _ := ts[0].Call(context.Background(), json.RawMessage(`{}`), nil)
			if res.IsError {
				t.Error(res.Flatten())
			}
		}()
	}
	wg.Wait()
	if calls != 2 {
		t.Fatalf("concurrent calls opened %d clients", calls)
	}
}
