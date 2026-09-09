package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcphttp"
	"github.com/Autumn-27/norma/mcp"
	actool "github.com/Autumn-27/norma/tool"
)

// mcpClient is the shared surface of a connected MCP server (stdio or remote http),
// so tools/list and cleanup are handled uniformly regardless of transport.
type mcpClient interface {
	Tools(context.Context) ([]actool.CoreTool, error)
	Close() error
}

type mcpDiscoveryPhase string

const (
	mcpPhaseConfig    mcpDiscoveryPhase = "config"
	mcpPhaseHandshake mcpDiscoveryPhase = "handshake"
	mcpPhaseTools     mcpDiscoveryPhase = "tools"
)

type mcpDiscoveryError struct {
	Phase mcpDiscoveryPhase
	Err   error
}

func (e *mcpDiscoveryError) Error() string { return e.Err.Error() }
func (e *mcpDiscoveryError) Unwrap() error { return e.Err }

// normalizeMCPServer validates an API or database MCP row and rewrites its JSON
// fields to one canonical representation. Transport-inapplicable fields are
// cleared so imports cannot leave an ambiguous command + URL configuration.
func normalizeMCPServer(m *db.MCPServer) error {
	if m == nil {
		return fmt.Errorf("MCP 配置为空")
	}
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if len(m.Name) > 128 {
		return fmt.Errorf("名称不能超过 128 个字符")
	}

	var args []string
	if len(m.Args) > 0 && string(m.Args) != "null" {
		var rawArgs []json.RawMessage
		if err := json.Unmarshal(m.Args, &rawArgs); err != nil {
			return fmt.Errorf("args 必须是字符串数组: %w", err)
		}
		args = make([]string, 0, len(rawArgs))
		for _, raw := range rawArgs {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return fmt.Errorf("args 必须是字符串数组: null 不是字符串")
			}
			var arg string
			if err := json.Unmarshal(raw, &arg); err != nil {
				return fmt.Errorf("args 必须是字符串数组: %w", err)
			}
			args = append(args, arg)
		}
	}
	if len(args) > 256 {
		return fmt.Errorf("args 最多包含 256 项")
	}
	for _, arg := range args {
		if len(arg) > 16*1024 {
			return fmt.Errorf("单个参数不能超过 16 KiB")
		}
	}

	env := map[string]string{}
	if len(m.Env) > 0 && string(m.Env) != "null" {
		var rawEnv map[string]json.RawMessage
		if err := json.Unmarshal(m.Env, &rawEnv); err != nil {
			return fmt.Errorf("env 必须是字符串键值对象: %w", err)
		}
		for key, raw := range rawEnv {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return fmt.Errorf("env 必须是字符串键值对象: %s 不能为 null", key)
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("env 必须是字符串键值对象: %w", err)
			}
			env[key] = value
		}
	}
	if len(env) > 256 {
		return fmt.Errorf("env 最多包含 256 项")
	}
	for key := range env {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("env 不能包含空键")
		}
	}

	m.Transport = strings.ToLower(strings.TrimSpace(m.Transport))
	m.Command = strings.TrimSpace(m.Command)
	m.URL = strings.TrimSpace(m.URL)
	switch m.Transport {
	case "stdio":
		if m.Command == "" {
			return fmt.Errorf("stdio 传输缺少命令")
		}
		m.URL = ""
	case "http":
		if m.URL == "" {
			return fmt.Errorf("http 传输缺少 URL")
		}
		u, err := url.ParseRequestURI(m.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("http 传输 URL 必须是有效的 http 或 https 地址")
		}
		m.Command = ""
		args = []string{}
	default:
		return fmt.Errorf("未知传输方式 %q", m.Transport)
	}
	m.Args, _ = json.Marshal(args)
	m.Env, _ = json.Marshal(env)
	return nil
}

// connectMCP dials one MCP server per its transport. Callers must Close the client.
func connectMCP(ctx context.Context, m *db.MCPServer) (mcpClient, error) {
	if err := normalizeMCPServer(m); err != nil {
		return nil, err
	}
	switch m.Transport {
	case "stdio":
		return mcp.NewStdioClient(ctx, m.Name, m.Command, jsonStrMap(m.Env), jsonStrSlice(m.Args)...)
	case "http":
		// env map doubles as HTTP headers (e.g. Authorization).
		return mcphttp.New(ctx, m.Name, m.URL, jsonStrMap(m.Env))
	}
	return nil, fmt.Errorf("未知传输方式 %q", m.Transport)
}

// discoverMCP performs the complete initialize + tools/list flow without any
// persistence. Both the temporary test endpoint and cached discovery use it.
func discoverMCP(ctx context.Context, m *db.MCPServer) ([]db.MCPTool, error) {
	if err := normalizeMCPServer(m); err != nil {
		return nil, &mcpDiscoveryError{Phase: mcpPhaseConfig, Err: err}
	}
	cl, err := connectMCP(ctx, m)
	if err != nil {
		return nil, &mcpDiscoveryError{Phase: mcpPhaseHandshake, Err: err}
	}
	defer cl.Close()
	ts, err := cl.Tools(ctx)
	if err != nil {
		return nil, &mcpDiscoveryError{Phase: mcpPhaseTools, Err: err}
	}
	prefix := "mcp__" + m.Name + "__"
	tools := make([]db.MCPTool, 0, len(ts))
	for _, t := range ts {
		name := strings.TrimPrefix(t.Name(), prefix)
		tools = append(tools, db.MCPTool{Name: name, Description: t.Description()})
	}
	return tools, nil
}

func mcpDiscoveryErrorMessage(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "MCP 可用性检测超时"
	}
	var phaseErr *mcpDiscoveryError
	if !errors.As(err, &phaseErr) {
		return "MCP 检测失败：" + err.Error()
	}
	switch phaseErr.Phase {
	case mcpPhaseConfig:
		return "配置错误：" + phaseErr.Err.Error()
	case mcpPhaseHandshake:
		return "MCP 握手失败：" + phaseErr.Err.Error()
	case mcpPhaseTools:
		return "工具发现失败：" + phaseErr.Err.Error()
	default:
		return "MCP 检测失败：" + phaseErr.Err.Error()
	}
}

// discoverAndCacheMCP connects to one MCP, lists its tools, and persists the tool
// names to mcp_tools_cache so the UI shows them without a live connection.
func (s *Server) discoverAndCacheMCP(ctx context.Context, m *db.MCPServer) error {
	tools, err := discoverMCP(ctx, m)
	if err != nil {
		return err
	}
	if err := s.m.pg.SaveMCPTools(m.ID, tools); err != nil {
		return err
	}
	log.Printf("[mcp] %s 发现 %d 个工具并已缓存", m.Name, len(tools))
	return nil
}

// discoverEmptyMCPsOnStartup fills the tool cache for any enabled MCP that has none
// yet (notably the seeded Playwright MCP on first run). Runs sequentially in one
// goroutine so we never spawn many stdio servers (npx) at once, and never blocks
// startup. Best-effort: a failure leaves the cache empty to retry next start.
func (s *Server) discoverEmptyMCPsOnStartup() {
	servers, err := s.m.pg.ListMCP()
	if err != nil {
		log.Printf("[mcp] 启动自动发现: 读取列表失败: %v", err)
		return
	}
	for _, m := range servers {
		if !m.Enabled || len(m.Tools) > 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
		if err := s.discoverAndCacheMCP(ctx, m); err != nil {
			log.Printf("[mcp] 启动自动发现 %s 失败: %v", m.Name, err)
		}
		cancel()
	}
}
