package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// Cache metadata only: clients, credentials and task-bound tool closures are never
// shared between runs. Configuration changes select a different fingerprint.
type mcpMetadata struct {
	name, description string
	schema            map[string]any
}
type mcpMetadataEntry struct {
	key   [32]byte
	until time.Time
	tools []mcpMetadata
}
type mcpRuntimeCache struct {
	mu      sync.Mutex
	entries map[int64]mcpMetadataEntry
}

func mcpConfigKey(m *db.MCPServer) [32]byte {
	raw, _ := json.Marshal([]any{m.ID, m.Name, m.Transport, m.Command, m.Args, m.Env, m.URL, m.Insecure})
	return sha256.Sum256(raw)
}

func (c *mcpRuntimeCache) prepare(ctx context.Context, m *db.MCPServer, dial func(context.Context, *db.MCPServer) (mcpClient, error)) ([]actool.CoreTool, func(), error) {
	key := mcpConfigKey(m)
	c.mu.Lock()
	entry, ok := c.entries[m.ID]
	c.mu.Unlock()
	r := &lazyMCPRun{config: *m, dial: dial}
	if !ok || entry.key != key || time.Now().After(entry.until) {
		ts, err := r.tools(ctx)
		if err != nil {
			r.close()
			return nil, nil, err
		}
		entry = mcpMetadataEntry{key: key, until: time.Now().Add(5 * time.Minute)}
		for _, t := range ts {
			// Deep-copy schemas; do not retain a live transport via CoreTool.
			raw, _ := json.Marshal(t.InputSchema())
			var schema map[string]any
			_ = json.Unmarshal(raw, &schema)
			entry.tools = append(entry.tools, mcpMetadata{t.Name(), t.Description(), schema})
		}
		c.mu.Lock()
		if c.entries == nil {
			c.entries = map[int64]mcpMetadataEntry{}
		}
		for id, e := range c.entries {
			if time.Now().After(e.until) {
				delete(c.entries, id)
			}
		}
		if len(c.entries) >= 128 {
			for id := range c.entries {
				delete(c.entries, id)
				break
			}
		}
		c.entries[m.ID] = entry
		c.mu.Unlock()
	}
	out := make([]actool.CoreTool, 0, len(entry.tools))
	for _, meta := range entry.tools {
		raw, _ := json.Marshal(meta.schema)
		var schema map[string]any
		_ = json.Unmarshal(raw, &schema)
		out = append(out, actool.Build(actool.Spec{
			Name: meta.name, Description: meta.description, Schema: schema,
			Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
				return permission.AskUser("call MCP tool " + meta.name + "?")
			},
			Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
				ts, err := r.tools(ctx)
				if err != nil {
					return actool.Errorf("MCP connection failed: " + err.Error()), nil
				}
				for _, t := range ts {
					if t.Name() == meta.name {
						return t.Call(ctx, in, tc)
					}
				}
				return actool.Errorf("MCP tool no longer available: " + meta.name), nil
			},
		}))
	}
	return out, r.close, nil
}

type lazyMCPRun struct {
	mu     sync.Mutex
	config db.MCPServer
	dial   func(context.Context, *db.MCPServer) (mcpClient, error)
	client mcpClient
	live   []actool.CoreTool
	closed bool
}

func (r *lazyMCPRun) tools(ctx context.Context) ([]actool.CoreTool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("MCP run closed")
	}
	if r.client != nil {
		return r.live, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cl, err := r.dial(ctx, &r.config)
	if err != nil {
		return nil, err
	}
	ts, err := cl.Tools(ctx)
	if err != nil {
		_ = cl.Close()
		return nil, err
	}
	r.client, r.live = cl, ts
	return ts, nil
}
func (r *lazyMCPRun) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.client != nil {
		_ = r.client.Close()
		r.client = nil
	}
}
