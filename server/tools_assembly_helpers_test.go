package server

import (
	"context"
	"github.com/Autumn-27/artex/agent"
	actool "github.com/Autumn-27/norma/tool"
	"testing"
)

func resolveToolsForTest(t *testing.T, ctx context.Context, role string, base []actool.CoreTool) []actool.CoreTool {
	t.Helper()
	out, err := agent.ToolResolve(ctx, role, base)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func augmentToolsForTest(t *testing.T, ctx context.Context, role string, base []actool.CoreTool) ([]actool.CoreTool, agent.DeferredInfo, func()) {
	t.Helper()
	out, def, cleanup, err := agent.AugmentTools(ctx, role, base)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	return out, def, cleanup
}
