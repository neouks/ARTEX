package agent

import (
	"context"
	"errors"
	actool "github.com/Autumn-27/norma/tool"
	"testing"
)

func TestToolAssemblyFailureAllRoles(t *testing.T) {
	oldAugment, oldResolve := ToolAugment, ToolResolve
	t.Cleanup(func() { ToolAugment, ToolResolve = oldAugment, oldResolve })
	want := errors.New("catalog unavailable")
	ToolResolve = func(context.Context, string, []actool.CoreTool) ([]actool.CoreTool, error) { return nil, want }
	for _, role := range []string{"worker", "planner", "mainagent", "retester", "custom"} {
		t.Run(role, func(t *testing.T) {
			closed := 0
			ToolAugment = func(context.Context, string) ([]actool.CoreTool, DeferredInfo, func()) {
				return []actool.CoreTool{actool.NewBash()}, DeferredInfo{GlobalNames: []string{"must-not-leak"}}, func() { closed++ }
			}
			out, def, cleanup, err := AugmentTools(context.Background(), role, []actool.CoreTool{actool.NewRead()})
			cleanup()
			if !errors.Is(err, want) || len(out) != 0 || len(def.GlobalNames) != 0 || closed != 1 {
				t.Fatalf("failure not closed: out=%d closed=%d err=%v", len(out), closed, err)
			}
		})
	}
}
