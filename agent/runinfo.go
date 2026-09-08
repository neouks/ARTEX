package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmrec"
)

// RunInfo identifies WHICH run a tool call belongs to. Tool assembly only receives
// (ctx, agentKey) — the task/exploration ids live in the caller's arguments, not the
// ctx — so anything wired at assembly time (currently the Skill ledger in
// server/assembly.go) has no way to attribute a call to a task. Each run attaches
// its own RunInfo before calling AugmentTools; the wiring closure reads it once and
// captures it, so per-run attribution stays correct without threading parameters
// through the tool layer. Same pattern as TaskClock (see taskclock.go).
//
// Zero value = attribution unknown; every consumer must treat it as optional.
type RunInfo = llmrec.RunInfo

// explorationID reads a store's exploration id, tolerating a nil store (planner and
// worker runs can be driven without one in tests).
func explorationID(ts *db.ExplorationStore) int64 {
	if ts == nil {
		return 0
	}
	return ts.ID()
}

// WithRunInfo attaches run attribution to ctx.
func WithRunInfo(ctx context.Context, ri RunInfo) context.Context {
	return llmrec.WithRunInfo(ctx, ri)
}

// RunInfoFrom reads the RunInfo (zero value if none attached).
func RunInfoFrom(ctx context.Context) RunInfo {
	return llmrec.RunInfoFrom(ctx)
}
