package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/agent"
	actool "github.com/Autumn-27/norma/tool"
)

func TestTaskPlannerAdvancesColdCompactionRounds(t *testing.T) {
	s, _ := newRetestServer(t)
	s.engine = NewEngine(s.m)
	s.taskAgents = map[string]*taskAgentBundle{}
	pt, err := s.m.pg.CreateTask("compactor wiring", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.m.pg.DeleteTask(pt.ID)
	task := &Task{ID: fmt.Sprint(pt.ID), ExpID: pt.ExplorationID, Store: s.m.pg.Exploration(pt.ExplorationID)}
	s.m.tasks[task.ID] = task
	// End the planning round after cold-state maintenance, without contacting an
	// LLM or executing tools. This exercises the actual per-task construction path.
	stop := errors.New("stop after maintenance")
	old := agent.ToolResolve
	agent.ToolResolve = func(context.Context, string, []actool.CoreTool) ([]actool.CoreTool, error) { return nil, stop }
	defer func() { agent.ToolResolve = old }()
	bundle := s.agentsForTask(task)
	for want := int64(1); want <= 2; want++ {
		if s.agentsForTask(task) != bundle {
			t.Fatal("task bundle not reused")
		}
		_, _, err = bundle.pl.Plan(t.Context(), pt.ID, s.m.Assets(), nil, task.Store, "test", nil, nil)
		if !errors.Is(err, stop) {
			t.Fatalf("unexpected planning error: %v", err)
		}
		got, err := task.Store.RoundNo()
		if err != nil || got != want {
			t.Fatalf("cold compactor not invoked: round=%d want=%d err=%v", got, want, err)
		}
	}
}
