package server

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestNormalizeMCPServer(t *testing.T) {
	m := &db.MCPServer{
		Name:      " demo ",
		Transport: "stdio",
		Command:   " npx ",
		Args:      json.RawMessage(`["-y","pkg"]`),
		Env:       json.RawMessage(`{"TOKEN":"x"}`),
		URL:       "https://ignored.example",
	}
	if err := normalizeMCPServer(m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "demo" || m.Command != "npx" || m.URL != "" {
		t.Fatalf("normalization = %+v", m)
	}
	var args []string
	if err := json.Unmarshal(m.Args, &args); err != nil || len(args) != 2 {
		t.Fatalf("args = %s (%v)", m.Args, err)
	}
	if err := json.Unmarshal(m.Env, &map[string]string{}); err != nil {
		t.Fatalf("env = %s (%v)", m.Env, err)
	}
}

func TestNormalizeMCPServerRejectsTransportSpecificErrors(t *testing.T) {
	cases := []struct {
		name string
		m    db.MCPServer
	}{
		{"missing stdio command", db.MCPServer{Name: "x", Transport: "stdio"}},
		{"missing http url", db.MCPServer{Name: "x", Transport: "http"}},
		{"invalid http url", db.MCPServer{Name: "x", Transport: "http", URL: "ftp://example.com"}},
		{"non-array args", db.MCPServer{Name: "x", Transport: "stdio", Command: "x", Args: json.RawMessage(`{"a":1}`)}},
		{"non-string env", db.MCPServer{Name: "x", Transport: "stdio", Command: "x", Env: json.RawMessage(`{"a":1}`)}},
		{"null env value", db.MCPServer{Name: "x", Transport: "stdio", Command: "x", Env: json.RawMessage(`{"a":null}`)}},
		{"null arg value", db.MCPServer{Name: "x", Transport: "stdio", Command: "x", Args: json.RawMessage(`[null]`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := normalizeMCPServer(&tc.m); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
