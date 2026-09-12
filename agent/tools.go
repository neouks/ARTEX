package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// balancedCap decides how many done-intents and facts to keep when the combined
// unfolded settled/cold region exceeds cap (§6.3). The smaller side is kept whole;
// the larger side takes the remaining budget; neither is starved below cap/2.
// Returns the keep counts and whether truncation applied. Newest-first slices, so
// callers keep the head.
func balancedCap(done, facts, cap int) (keepDone, keepFacts int, truncated bool) {
	if done+facts <= cap {
		return done, facts, false
	}
	half := cap / 2
	keepDone, keepFacts = done, facts
	switch {
	case done > half && facts > half:
		keepDone, keepFacts = half, cap-half
	case done > half:
		keepDone = cap - facts // facts fit in ≤half; intents take the rest
	default:
		keepFacts = cap - done // intents fit in ≤half; facts take the rest
	}
	return keepDone, keepFacts, true
}

// compactIntents distills intents to {id, summary, state, asset_ids, parents,
// yields} so the planner sees both the direction and its LINEAGE — parents (the
// upstream nodes it derived from: facts/intents/findings) and yields (the facts/
// findings it produced) — without pulling full payloads. parentsOf/yieldsOf are
// built from the exploration edges in graph_overview.
func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64) []map[string]any {
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m := map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State}
		if n.Inherited {
			m["source_task_id"] = n.SourceTaskID
			m["inherited"] = true
		}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok := p["asset_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_id"]; ok && tg != nil && tg != "" {
			m["asset_ids"] = []any{tg}
		}
		if ps := parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // 上游：本意图派生自哪些节点（多个事实可共同产生一个意图）
		}
		if ys := yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // 下游：本意图产生了哪些事实/发现
		}
		out = append(out, m)
	}
	return out
}

func visibleNodeIDs(groups ...[]*db.Node) map[int64]struct{} {
	visible := make(map[int64]struct{})
	for _, nodes := range groups {
		for _, node := range nodes {
			if node != nil {
				visible[node.ID] = struct{}{}
			}
		}
	}
	return visible
}

// restrictNodeRelations removes every edge endpoint that was filtered out by
// task-asset authorization. Without this, graph_overview could hide a pending
// node's payload while still exposing its numeric id through parents/yields.
func restrictNodeRelations(relations map[int64][]int64, visible map[int64]struct{}) map[int64][]int64 {
	filtered := make(map[int64][]int64, len(relations))
	for from, targets := range relations {
		if _, ok := visible[from]; !ok {
			continue
		}
		for _, target := range targets {
			if _, ok := visible[target]; ok {
				filtered[from] = append(filtered[from], target)
			}
		}
	}
	return filtered
}

func restrictNodeOrigins(origins map[int64]int64, visible map[int64]struct{}) map[int64]int64 {
	filtered := make(map[int64]int64, len(origins))
	for nodeID, originID := range origins {
		if _, ok := visible[nodeID]; !ok {
			continue
		}
		if _, ok := visible[originID]; ok {
			filtered[nodeID] = originID
		}
	}
	return filtered
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	findingRecorder FindingRecorder
	overviewReads   *overviewReadScope // only set on a copy for one overview call
	as              *db.AssetStore     // asset store (optional; nil = asset tools not available)
	cs              *db.CompanyStore   // company store (optional)
	ts              *db.ExplorationStore
	worker          string
	taskID          int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// coverageDisabled mirrors tasks.coverage_enabled=false. Stored inverted so the
	// zero value (all existing ToolSet constructions) means ENABLED — matching the
	// DB default (true). When true: graphOverviewData drops the coverage block, the
	// auto-scope hook (insertAssets) is skipped, and add_task_scope/list_untested_assets
	// are filtered out of the agent's tool list. The scope field stays regardless.
	coverageDisabled bool
	// Worker discoveries are tracked separately from the claimed intent's plan
	// anchors. Planner discoveries retain their exploration lineage.
	ownerNode       int64
	GoalMet         bool
	Reason          string
	writes          WriteCounts
	workerExecution bool // set only by Worker.execute, never by tool arguments/catalog
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
	// notifyFinding, if set, wakes the task's planner when this run reports a finding,
	// carrying (intentID, summary) so the round can spell out which intent found what.
	// Wired for workers; nil elsewhere → falls back to notify (bare wake).
	notifyFinding func(intentID int64, summary string)
	// resumeTask, if set, revives the task after a graph change that should make a
	// stopped task run again (currently: set_goals adds a goal). It flips a terminal/
	// paused task back to running and (re)starts the engine loops — a plain notify()
	// can't, because the planner's terminal gate swallows wakes. Wired ONLY for the
	// main agent (human steering); nil for the goals decomposer and workers.
	resumeTask func()
	// notifyGoal, if set, wakes the planner AND records ONE "人新增了 N 个目标：…" trigger
	// for a whole set_goals call (batch-aware — one call, one trigger, not one per goal)
	// so the next round spells out the added goals (instead of the planner having to
	// spot new open goals in the overview). Wired ONLY for the main agent; nil for the
	// goals decomposer (round-0 has no running planner to inform) and workers → those
	// fall back to the bare notify.
	notifyGoal func(texts []string)
}

// SetNotifyGoal wires the goal-add trigger callback (see ToolSet.notifyGoal). Set only
// by the main-agent chat, so runtime-added goals are announced to the planner by name.
func (t *ToolSet) SetNotifyGoal(fn func([]string)) { t.notifyGoal = fn }

// SetResumeTask wires the task-revive callback (see ToolSet.resumeTask). Set only by
// the main-agent chat, so runtime-added goals can pull a finished task back to running.
func (t *ToolSet) SetResumeTask(fn func()) { t.resumeTask = fn }

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// SetNotifyFinding wires the finding-wake callback (see ToolSet.notifyFinding).
func (t *ToolSet) SetNotifyFinding(fn func(int64, string)) { t.notifyFinding = fn }

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings; each element of a batch counts once).
type WriteCounts struct {
	Facts    int
	Assets   int
	Findings int
}

// Total is every node persisted this run, regardless of kind — the
// "explored but persisted nothing" signal (Total == 0).
func (w WriteCounts) Total() int { return w.Facts + w.Assets + w.Findings }

// String renders the per-kind breakdown for logs, e.g. "事实1 资产25 漏洞0".
func (w WriteCounts) String() string {
	return fmt.Sprintf("事实%d 资产%d 漏洞%d", w.Facts, w.Assets, w.Findings)
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// SetCoverageEnabled records whether this task has the asset-coverage feature on
// (default enabled). Passing false makes graphOverviewData omit the coverage block,
// the insertAssets auto-scope hook a no-op, and CoverageTools reports the two
// coverage-only tools so callers can drop them from the agent's tool list.
func (t *ToolSet) SetCoverageEnabled(enabled bool) { t.coverageDisabled = !enabled }

// CoverageDisabled reports whether the coverage feature is off for this task.
func (t *ToolSet) CoverageDisabled() bool { return t.coverageDisabled }

// coverageOnlyTools are the LLM tools that only make sense when asset coverage is
// on. When the feature is off they are filtered out of the agent's tool list so
// they neither pollute the prompt nor let the model build a disabled denominator.
var coverageOnlyTools = map[string]bool{"add_task_scope": true, "list_untested_assets": true}

// DropCoverageTools returns tools with the coverage-only ones removed when this
// task has the feature disabled; otherwise it returns tools unchanged.
func (t *ToolSet) DropCoverageTools(tools []actool.CoreTool) []actool.CoreTool {
	if !t.coverageDisabled {
		return tools
	}
	out := tools[:0:0]
	for _, tool := range tools {
		if coverageOnlyTools[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// StripCoverageParams hides coverage-only parameters (currently insert_assets'
// per-item `related`, which only decides whether an asset enters the coverage
// denominator) from the model-facing schema when coverage is disabled — the param
// has no effect then, so showing it just pollutes the prompt. MUST run on the FINAL
// tool list (after AugmentTools/ToolResolve): the DB tools table is authoritative on
// schema, so stripping the code schema earlier would be overwritten. No-op when
// enabled or when the list has no insert_assets. The schema is deep-copied before
// editing so the shared/DB schema map is never mutated.
func (t *ToolSet) StripCoverageParams(tools []actool.CoreTool) []actool.CoreTool {
	if !t.coverageDisabled {
		return tools
	}
	for i, tool := range tools {
		if tool.Name() != "insert_assets" {
			continue
		}
		schema := deepCopyJSONMap(tool.InputSchema())
		if props, ok := nestedMap(schema, "properties", "assets", "items", "properties"); ok {
			delete(props, "related")
		}
		tools[i] = DecorateTool(tool, tool.Description(), schema)
	}
	return tools
}

// deepCopyJSONMap returns a JSON round-trip deep copy of a schema map so callers can
// edit it without touching the original (which may be shared/cached). Falls back to
// the input on any marshal error (edits then become best-effort no-ops upstream).
func deepCopyJSONMap(m map[string]any) map[string]any {
	b, err := json.Marshal(m)
	if err != nil {
		return m
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		return m
	}
	return out
}

// nestedMap walks a chain of string keys through nested map[string]any values,
// returning the final map and whether the whole path resolved to one.
func nestedMap(m map[string]any, keys ...string) (map[string]any, bool) {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(…,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool      { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool       { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool     { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool   { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) NodeDetailTool() actool.CoreTool         { return t.nodeDetail() }
func (t *ToolSet) AddHintTool() actool.CoreTool            { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// anchorOwner records Worker execution provenance separately from plan anchors.
// Other roles retain their existing exploration lineage.
func (t *ToolSet) anchorOwner(assetID int64) error {
	if t.workerExecution && t.as != nil {
		return t.as.RememberWorkerAccess(t.taskID, t.ownerNode, nil, []int64{assetID})
	}
	if t.ownerNode > 0 && assetID > 0 {
		return t.ts.Anchor(t.ownerNode, assetID)
	}
	return nil
}

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r := range raw {
		if v := pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return readTool("graph_overview",
		"(探索链路图)探索态势蒸馏摘要：资产计数、无接口的站点、frontier、发现、hints(人类/主 agent 的战略提示，生成意图时须纳入)。Planner 每轮已预取；仅在需要刷新时调用。",
		obj(map[string]any{}),
		func(_ context.Context, raw json.RawMessage) (actool.Result, error) {
			var args struct{}
			if err := decodeToolInput(raw, &args); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if t == nil || t.ts == nil {
				return actool.Errorf("graph_overview 需要任务探索上下文，当前工具未绑定任务"), nil
			}
			return jsonResult(t.graphOverviewData())
		})
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func (t *ToolSet) graphOverviewData() map[string]any {
	if t == nil || t.ts == nil {
		return map[string]any{"error": "graph_overview 需要任务探索上下文，当前工具未绑定任务"}
	}
	local := *t
	local.overviewReads = &overviewReadScope{states: map[int64]map[int64]string{}}
	t = &local
	out := map[string]any{}
	unavailable := map[string]string{}
	markError := func(err error, keys ...string) {
		if err != nil {
			for _, key := range keys {
				unavailable[key] = err.Error()
			}
		}
	}
	defer func() {
		if len(unavailable) > 0 {
			for key := range unavailable {
				delete(out, key)
			}
			out["unavailable"] = unavailable
			out["partial"] = true
		}
	}()
	// goals summary folded in so the planner needn't call list_goals each round.
	goals, goalsErr := t.ts.ToolNodesByKind(db.KindGoal, 100)
	markError(goalsErr, "goals")
	goals, goalsAuthErr := t.authorizedNodesForStore(goals, t.ts, t.taskID)
	markError(goalsAuthErr, "goals")
	gsum := make([]map[string]any, 0, len(goals))
	for _, g := range goals {
		var p map[string]any
		_ = json.Unmarshal(g.Payload, &p)
		gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
	}
	out["goals"] = gsum
	// hints: 人类/主 agent 通过 add_hint 挂上图的战略提示；folded in so the
	// planner reads them every round when generating intents (否则只写不读).
	hints, hintsErr := t.ts.ToolNodesByKind(db.KindHint, 50)
	markError(hintsErr, "hints")
	hints, hintsAuthErr := t.authorizedNodesForStore(hints, t.ts, t.taskID)
	markError(hintsAuthErr, "hints")
	hsum := make([]map[string]any, 0, len(hints))
	for _, h := range hints {
		var p map[string]any
		_ = json.Unmarshal(h.Payload, &p)
		hint := map[string]any{"id": h.ID, "state": h.State, "text": p["text"]}
		if findingTrafficBindingEnabled() && p["traffic_refs"] != nil {
			hint["traffic_refs"] = p["traffic_refs"]
		}
		hsum = append(hsum, hint)
	}
	out["hints"] = hsum
	// lineage from the exploration edges: an intent's parents (what it
	// derived_from — possibly several facts combined) and its yields (the
	// facts/findings it produced). factFrom maps a fact → the intent that
	// produced it. This is the relationship layer the flat lists lacked.
	edges, edgesErr := t.ts.Edges(5000)
	markError(edgesErr, "lineage")
	parentsOf := map[int64][]int64{}
	yieldsOf := map[int64][]int64{}
	factFrom := map[int64]int64{}
	for _, e := range edges {
		switch e.Rel {
		case db.RelDerivedFrom, db.RelSpawns: // upstream: derived_from (fact/finding/intent→intent) or spawns (origin fact→goal, legacy begin→intent)
			parentsOf[e.To] = append(parentsOf[e.To], e.From)
		case db.RelYields: // intent --yields--> fact/finding
			yieldsOf[e.From] = append(yieldsOf[e.From], e.To)
			factFrom[e.To] = e.From
		}
	}
	// cold-digest §6: members folded into an active digest are shown via cold_digests
	// (below), not the flat recent_* lists. `covered` maps member id → its digest id.
	// §6 render-time revival check: a covered member that has become hot again (a new
	// intent derived from it) must reappear this round — so `hidden` folds a member out
	// only when it is covered AND still cold. covered_members (built from hidden) lets a
	// parents/yields id pointing into a live fold stay resolvable (§6.5 dangling lineage).
	covered := t.authorizedCoveredMembers(t.ts, t.taskID)
	// Hot set at render time serves two §6 needs: (1) a covered member that revived
	// (now hot) must reappear this round; (2) the 60-cap must never truncate hot
	// (active-context) nodes — only the cold-but-unfolded region is cappable (§6.3).
	// Computed every round (cheap for real graph sizes); nil map degrades safely.
	var hotAtRender map[int64]bool
	if cg, _, err := loadColdGraph(t.ts); err == nil {
		hotAtRender = cg.hotSet()
	}
	hidden := func(id int64) bool { _, c := covered[id]; return c && !hotAtRender[id] }
	fr, frontierErr := t.ts.Frontier(100)
	markError(frontierErr, "frontier_open", "open_intents")
	fr, frontierAuthErr := t.authorizedNodesForStore(fr, t.ts, t.taskID)
	markError(frontierAuthErr, "frontier_open", "open_intents")
	all, intentsErr := t.ts.ToolNodesByKind(db.KindIntent, 300)
	markError(intentsErr, "running_intents", "recent_done_intents", "done_intents_total", "done_intents_in_window")
	all, intentsAuthErr := t.authorizedNodesForStore(all, t.ts, t.taskID)
	markError(intentsAuthErr, "running_intents", "recent_done_intents", "done_intents_in_window")
	var running, recentDone []*db.Node
	doneTotal := 0
	for _, n := range all {
		switch n.State {
		case "running":
			running = append(running, n)
		case "done", "blocked", "exhausted", "stopped":
			doneTotal++
			if hidden(n.ID) {
				continue // in a cold_digest and still cold — shown via cold_digests (§6.2)
			}
			recentDone = append(recentDone, n) // §6: no 15-cap; folded ones are gone, cap applied below
		}
	}
	// recent_done_intents is finalized further below (after facts are known) so the
	// 60-cap can balance the two lists and protect hot nodes (§6.3).
	// done_intents_total：已结束意图（done/blocked/exhausted）总数，与 recent_done_intents
	// 平行命名——后者只是它的最新窗口（≤15）截断视图。两键并排即自描述："看到的是 N/总数"，
	// 让 planner 去重时别把"没显示"当成"没派过"，无需在提示词里另行解释。
	if t.as == nil || t.taskID <= 0 {
		if total, err := t.ts.CountFinishedIntents(); err == nil {
			out["done_intents_total"] = total
		} else {
			markError(err, "done_intents_total")
		}
	} else {
		out["done_intents_in_window"] = doneTotal
	}
	out["frontier_open"] = len(fr)
	// findings (confirmed vulns) and facts (worker exploration results) are
	// now distinct node kinds. recent_facts surfaces fact summaries (esp.
	// negative results) so the planner sees them in one call; full content
	// via node_detail(id).
	vulnPage, vulnErr := t.ts.ToolLocalNodePage(context.Background(), db.KindFinding, 20)
	factPage, factErr := t.ts.ToolLocalNodePage(context.Background(), db.KindFact, 1000)
	markError(vulnErr, "findings", "finding_list")
	markError(factErr, "facts", "recent_facts")
	vulnNodes := vulnPage.Nodes[:min(20, len(vulnPage.Nodes))]
	factNodes := factPage.Nodes[:min(1000, len(factPage.Nodes))]
	visible := visibleNodeIDs(goals, hints, fr, all, vulnNodes, factNodes)
	parentsOf = restrictNodeRelations(parentsOf, visible)
	yieldsOf = restrictNodeRelations(yieldsOf, visible)
	factFrom = restrictNodeOrigins(factFrom, visible)
	out["open_intents"] = compactIntents(fr, parentsOf, yieldsOf)
	out["running_intents"] = compactIntents(running, parentsOf, yieldsOf)
	out["findings"] = vulnPage.Total
	out["facts"] = factPage.Total
	if vulnPage.Total > len(vulnNodes) {
		out["finding_list_truncated"] = true
		out["finding_list_read_hint"] = "更多摘要请用 list_findings"
	}
	// 概览仅预取有限漏洞摘要及关联资产；完整列表按需 list_findings 分页，
	// 证据正文通过 node_detail 读取，不因概览窗口未包含某项就断言其不存在。
	var findingMeta map[int64]db.FindingMeta // node_id -> 锚定资产等；仅任务上下文可查
	assetByID := map[int64]*db.Asset{}
	if t.as != nil && t.taskID > 0 {
		var metaErr error
		findingMeta, metaErr = t.as.FindingMetaByNodeID(t.taskID)
		markError(metaErr, "finding_asset_metadata")
		idSet := map[int64]struct{}{}
		for _, node := range vulnNodes {
			meta := findingMeta[node.ID]
			for _, aid := range meta.AssetIDs {
				idSet[aid] = struct{}{}
			}
		}
		if len(idSet) > 0 {
			ids := make([]int64, 0, len(idSet))
			for aid := range idSet {
				ids = append(ids, aid)
			}
			if assets, err := t.as.GetByIDs(ids); err == nil {
				for _, a := range assets {
					assetByID[a.ID] = a
				}
			}
		}
	}
	findingList := make([]map[string]any, 0, len(vulnNodes))
	for _, n := range vulnNodes {
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		m := map[string]any{"id": n.ID, "summary": fp["summary"]}
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 本漏洞由哪个意图产生
		}
		if meta, ok := findingMeta[n.ID]; ok && len(meta.AssetIDs) > 0 {
			assets := make([]string, 0, len(meta.AssetIDs))
			for _, aid := range meta.AssetIDs {
				if a := assetByID[aid]; a != nil {
					if v := assetValue(a); v != "" {
						assets = append(assets, v)
						continue
					}
				}
				assets = append(assets, fmt.Sprintf("#%d", aid)) // 资产已删/查不到 → 退回 id 标记，别丢信息
			}
			m["assets"] = assets // 受影响资产的可读内容
		}
		findingList = append(findingList, m)
	}
	out["finding_list"] = findingList
	// Build facts, split hot (active context — a fact under a live intent) from cold
	// (not-yet-folded). Hidden (folded & still cold) ones are surfaced via cold_digests.
	var recentFactsHot, recentFactsCold []map[string]any
	for _, n := range factNodes {
		if hidden(n.ID) {
			continue // in a cold_digest and still cold — surfaced via cold_digests (§6.2)
		}
		m := compactNode(n)
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 本事实由哪个意图产生
		}
		// confidence 带进概览：让规划者一眼看出哪条结论只是 inferred（尤其否定结论
		// 别当铁案）；evidence 较长，留给 node_detail(id)。
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) == nil {
			if c, ok := fp["confidence"].(string); ok && c != "" {
				m["confidence"] = c
			}
		}
		if hotAtRender[n.ID] {
			recentFactsHot = append(recentFactsHot, m)
		} else {
			recentFactsCold = append(recentFactsCold, m)
		}
	}
	// Split the settled intents the same way: hot = ancestor of a live intent (active
	// context); cold = not-yet-folded settled.
	var doneHot, doneCold []*db.Node
	for _, n := range recentDone {
		if hotAtRender[n.ID] {
			doneHot = append(doneHot, n)
		} else {
			doneCold = append(doneCold, n)
		}
	}
	// §6.3 兜底截断：hot（活跃探索上下文——live 前沿的祖先链、live 意图下的新事实）【永不】
	// 被截；60-cap 只约束【冷但未折】的残余（压缩滞后时才增长）。live(open/running) 另有字段、
	// 同样不受影响。冷区两侧均衡保留：较少一侧全留、较多一侧填余额，各自保底 cap/2，绝不饿到 0。
	const unfoldedCap = 60
	keepColdDone, keepColdFacts, truncated := balancedCap(len(doneCold), len(recentFactsCold), unfoldedCap)
	if truncated {
		out["unfolded_truncated"] = true // 正常不触发；触发说明压缩滞后
	}
	out["recent_done_intents"] = append(
		compactIntents(doneHot, parentsOf, yieldsOf),
		compactIntents(doneCold[:keepColdDone], parentsOf, yieldsOf)...)
	out["recent_facts"] = append(recentFactsHot, recentFactsCold[:keepColdFacts]...) // {id, summary, from_intent, confidence?}；详情用 node_detail(id)
	// cold-digest §6.1/§6.2: the folded cold region + the per-asset directory that
	// collapses independent directions, plus the dangling-lineage resolver map.
	cds, cidx, coldErr := t.coldDigestOverview()
	markError(coldErr, "cold_digests", "cold_index", "covered_members")
	if len(cds) > 0 {
		out["cold_digests"] = cds // [{id, body, member_count}] —— 直接读 body (§6.1)
		out["cold_index"] = cidx  // [{asset, asset_id, digest_ids}] —— 按资产收敛方向 (§6.2)
	}
	if len(covered) > 0 {
		cm := make(map[string]int64, len(covered))
		for member, dig := range covered {
			if hotAtRender[member] {
				continue // revived → shown live this round, not a dangling folded id
			}
			cm[strconv.FormatInt(member, 10)] = dig
		}
		if len(cm) > 0 {
			out["covered_members"] = cm // 悬空血缘 id → 它属于哪个 digest；用 expand_digest 展开 (§6.5)
		}
	}
	// the original task (root) so the planner always has it, not just the
	// decomposed goals.
	if description, goal, err := t.ts.Root(); err == nil {
		out["task"] = map[string]any{"description": description, "goal": goal}
	} else {
		markError(err, "task")
	}
	// Direct source tasks are a live, read-only blackboard view. Keep their
	// summaries in a separate field so their intents never enter this task's
	// frontier or get mistaken for locally claimable work.
	related, relatedErr := t.relatedTaskOverviews()
	markError(relatedErr, "related_tasks")
	out["related_tasks"] = related
	// coverage：粗略的资产测试覆盖度参考——范围(task_scope)内的资产里，被 fact 碰过的
	// 占比 + by_type(按类型的 总数/已测)。要看未测的具体资产由 agent 按需调 list_untested_assets 自行判断。仅任务上下文有。
	// 资产覆盖度功能关闭时(coverageDisabled)：只保留 scope/hosts(范围边界与目标主机的
	// 感知信息，company 关联经由 scope 在此浮现)，丢弃 denominator/tested/pct/by_type/note
	// 等覆盖度度量，避免污染上下文、也不诱导已隐藏的 add_task_scope/list_untested_assets。
	if t.as != nil && t.ts != nil && t.taskID > 0 {
		{
			m := map[string]any{}
			if !t.coverageDisabled {
				if cov, err := t.as.TaskCoverageWithSources(t.taskID); err == nil {
					m["denominator"] = cov.Denominator
					m["tested"] = cov.Tested
					m["by_type"] = cov.ByType
					m["note"] = "coverage资产测试覆盖度（包括接口等各种相关资产），粗略估计、仅供参考：包含当前任务与直接关联任务的 scope、事实锚点；关联 scope 只读。容器型资产/大量枚举会让它偏低，勿据此认为已测完；可用 add_task_scope 增补本任务范围、list_untested_assets 看未测资产【通常不调用list_untested_assets，按照任务推进即可】；"
					if cov.Denominator == 0 {
						m["pct"] = nil
						m["status"] = "范围未锚定"
					} else {
						m["pct"] = cov.Pct
					}
				}
			}
			// scope：当前测试范围的根资产（task_scope 原始行），让 agent 知道这个任务到底
			// 圈定了哪些目标（不是全部测试资产，而是范围边界本身）。覆盖度开关无关，始终提供。
			if rows, err := t.as.ListTaskScopeWithSources(t.taskID); err == nil && len(rows) > 0 {
				rows = t.authorizedScope(rows)
				scope := make([]map[string]any, 0, len(rows))
				for _, r := range rows {
					e := map[string]any{"kind": r.Kind, "source": r.Source, "task_id": r.TaskID}
					if r.TaskID != t.taskID {
						inheritedMap(e, r.TaskID)
					}
					switch {
					case r.Domain != "":
						e["value"] = r.Domain
					case r.Net != "":
						e["value"] = r.Net
					case r.Value != "":
						e["value"] = r.Value
					case r.CompanyID != nil:
						e["company_id"] = *r.CompanyID
						if t.cs != nil {
							if company, err := t.cs.GetCompany(*r.CompanyID); err == nil && company != nil {
								e["company_name"] = company.Name
							}
							if rules, err := t.cs.GetScope(*r.CompanyID); err == nil {
								keywords := make([]string, 0)
								companyScope := make([]map[string]any, 0, len(rules))
								for _, rule := range rules {
									value := rule.Raw
									if value == "" {
										switch rule.Kind {
										case "domain":
											value = rule.Domain
										case "ip", "cidr":
											value = rule.Net
										default:
											value = rule.Value
										}
									}
									entry := map[string]any{"kind": rule.Kind, "value": value}
									if rule.Reason != "" {
										entry["reason"] = rule.Reason
									}
									companyScope = append(companyScope, entry)
									if rule.Kind == "keyword" && rule.Raw != "" {
										keywords = append(keywords, rule.Raw)
									}
								}
								if len(companyScope) > 0 {
									e["company_scope"] = companyScope
								}
								if len(keywords) > 0 {
									e["company_keywords"] = keywords
								}
							}
						}
					}
					scope = append(scope, e)
				}
				m["scope"] = scope
			}
			if count, err := t.as.CountHostsByTaskWithSources(t.taskID); err == nil {
				// 只给主机总数，不再把 host 列表平铺进 graph_overview（大范围任务里那是每轮
				// 都重复携带的大量字符串，对规划决策价值有限）；具体主机按需 list_assets 查。
				m["host_count"] = count
			} else {
				markError(err, "host_count")
			}
			if len(m) > 0 {
				out["coverage"] = m
			}
		}
	}
	if t.as != nil && t.taskID > 0 {
		if summary, err := t.as.QueryTaskAssetView(t.taskID, db.TaskAssetViewQuery{Status: "all", SummaryOnly: true}); err == nil {
			out["asset_approval_counts"] = summary.Counts
		} else {
			out["asset_approval_error"] = "审批计数暂不可用：" + err.Error()
		}
	}
	return out
}

func (t *ToolSet) filterAuthorizedNodesForStore(nodes []*db.Node, store *db.ExplorationStore, ownerTaskID int64) []*db.Node {
	out, _ := t.authorizedNodesForStore(nodes, store, ownerTaskID)
	return out
}

func (t *ToolSet) authorizedNodesForStore(nodes []*db.Node, store *db.ExplorationStore, ownerTaskID int64) ([]*db.Node, error) {
	if t.as == nil || t.taskID <= 0 || store == nil {
		return nodes, nil
	}
	nodeIDs := make([]int64, 0, len(nodes))
	for _, node := range nodes {
		nodeIDs = append(nodeIDs, node.ID)
	}
	lineage, err := store.LineageAnchorAssetIDsForNodes(nodeIDs)
	if err != nil {
		return nil, err
	}
	unique := make(map[int64]bool)
	var assetIDs []int64
	for _, node := range nodes {
		if len(lineage[node.ID]) == 0 {
			lineage[node.ID] = intentAssetIDs(node)
		}
		for _, id := range lineage[node.ID] {
			if !unique[id] {
				unique[id] = true
				assetIDs = append(assetIDs, id)
			}
		}
	}
	ownerStates, err := t.readApprovalStates(ownerTaskID, assetIDs)
	if err != nil {
		return nil, err
	}
	currentStates := ownerStates
	if ownerTaskID != t.taskID {
		currentStates, err = t.readApprovalStates(t.taskID, assetIDs)
		if err != nil {
			return nil, err
		}
	}
	out := make([]*db.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Kind == db.KindDigest {
			allowed, _, err := t.digestAuthorizationBatch(store, ownerTaskID, []int64{n.ID})
			if err != nil {
				return nil, err
			}
			if !allowed[n.ID] {
				continue
			}
		}
		approved := true
		for _, id := range lineage[n.ID] {
			if ownerStates[id] != db.ApprovalApproved || currentStates[id] != db.ApprovalApproved {
				approved = false
				break
			}
		}
		if approved {
			out = append(out, n)
		}
	}
	return out, nil
}

func (t *ToolSet) nodeAuthorized(n *db.Node) bool {
	ok, _ := t.nodeAuthorization(n)
	return ok
}

func (t *ToolSet) nodeAuthorization(n *db.Node) (bool, error) {
	if n == nil || t.as == nil || t.taskID <= 0 {
		return n != nil, nil
	}
	if !n.Inherited {
		nodes, err := t.authorizedNodesForStore([]*db.Node{n}, t.ts, t.taskID)
		return len(nodes) == 1, err
	}
	sources, err := t.directSourceStores()
	if err != nil {
		return false, err
	}
	for _, source := range sources {
		if source.Task.TaskID == n.SourceTaskID {
			nodes, err := t.authorizedNodesForStore([]*db.Node{n}, source.Store, source.Task.TaskID)
			return len(nodes) == 1, err
		}
	}
	return false, nil
}

func (t *ToolSet) nodeLineageAssetIDs(n *db.Node) ([]int64, error) {
	if n == nil || t.ts == nil {
		return nil, nil
	}
	if !n.Inherited {
		return t.ts.LineageAnchorAssetIDs(n.ID)
	}
	sources, err := t.directSourceStores()
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		if source.Task.TaskID == n.SourceTaskID {
			return source.Store.LineageAnchorAssetIDs(n.ID)
		}
	}
	return nil, nil
}

func inheritedMap(m map[string]any, sourceTaskID int64) map[string]any {
	m["source_task_id"] = sourceTaskID
	m["inherited"] = true
	return m
}

const (
	relatedOverviewTotalTextRunes     = 16_000
	relatedOverviewMaxTextPerSource   = 4_000
	relatedOverviewMaxGoalsPerSource  = 8
	relatedOverviewMaxHintsPerSource  = 6
	relatedOverviewMaxFactsPerSource  = 12
	relatedOverviewMaxFindingsPerTask = 6
	relatedOverviewMaxIntentsPerTask  = 8
	relatedOverviewMaxScopePerSource  = 12
)

// overviewTextBudget bounds inherited prompt text while preserving a fair slice
// for every direct source. Full evidence remains available through the on-demand
// read tools, so truncation here does not discard persisted blackboard data.
type overviewTextBudget struct {
	remaining int
	truncated bool
}

func relatedOverviewBudgetForSources(sourceCount int) int {
	if sourceCount <= 0 {
		return 0
	}
	if sourceCount > db.MaxTaskSourceCount {
		sourceCount = db.MaxTaskSourceCount
	}
	perSource := relatedOverviewTotalTextRunes / sourceCount
	if perSource > relatedOverviewMaxTextPerSource {
		perSource = relatedOverviewMaxTextPerSource
	}
	return perSource
}

func (b *overviewTextBudget) take(value any, fieldLimit int) string {
	var text string
	switch value := value.(type) {
	case string:
		text = strings.TrimSpace(value)
	case nil:
		return ""
	default:
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "" {
		return ""
	}
	if b.remaining <= 0 || fieldLimit <= 0 {
		b.truncated = true
		return ""
	}
	runes := []rune(text)
	limit := fieldLimit
	if limit > b.remaining {
		limit = b.remaining
	}
	if len(runes) > limit {
		b.truncated = true
		if limit == 1 {
			text = "…"
		} else {
			text = string(runes[:limit-1]) + "…"
		}
		runes = []rune(text)
	}
	b.remaining -= len(runes)
	return text
}

// relatedTaskOverviews distills persistent blackboard state from direct source
// tasks. It intentionally reads each source's local store methods, never its own
// related sources, so inheritance is one level only.
func (t *ToolSet) relatedTaskOverviews() ([]map[string]any, error) {
	sources, err := t.directSourceStores()
	if err != nil {
		return nil, err
	}
	if len(sources) > db.MaxTaskSourceCount {
		sources = sources[:db.MaxTaskSourceCount]
	}
	perSourceTextBudget := relatedOverviewBudgetForSources(len(sources))
	out := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		ts := source.Store
		// §2 cross-task: render the source task's OWN folded view — fold out the
		// members it has already folded, and surface its cold_digests read-only.
		hidden := t.hiddenMembersFor(ts, source.Task.TaskID)
		budget := overviewTextBudget{remaining: perSourceTextBudget}
		item := map[string]any{
			"source_task_id": source.Task.TaskID,
			"inherited":      true,
			"task": map[string]any{
				"description": budget.take(source.Task.Description, 800),
				"goal":        budget.take(source.Task.Goal, 800),
				"status":      source.Task.Status,
			},
		}
		sourceErrors := map[string]string{}
		note := func(err error, key string) {
			if err != nil {
				sourceErrors[key] = err.Error()
			}
		}
		readNodes := func(kind string, limit int) []*db.Node {
			nodes, err := ts.ToolNodesByKind(kind, limit)
			note(err, kind)
			if err != nil {
				return nil
			}
			nodes, err = t.authorizedNodesForStore(nodes, ts, source.Task.TaskID)
			note(err, kind)
			return nodes
		}
		stats, statsErr := ts.Stats()
		if t.as == nil {
			note(statsErr, "node_stats")
		}

		edges, edgesErr := ts.Edges(5000)
		note(edgesErr, "lineage")
		parentsOf := map[int64][]int64{}
		yieldsOf := map[int64][]int64{}
		factFrom := map[int64]int64{}
		for _, edge := range edges {
			switch edge.Rel {
			case db.RelDerivedFrom, db.RelSpawns:
				parentsOf[edge.To] = append(parentsOf[edge.To], edge.From)
			case db.RelYields:
				yieldsOf[edge.From] = append(yieldsOf[edge.From], edge.To)
				factFrom[edge.To] = edge.From
			}
		}

		goals := readNodes(db.KindGoal, relatedOverviewMaxGoalsPerSource)
		goalSummary := make([]map[string]any, 0, len(goals))
		for _, goal := range goals {
			var payload map[string]any
			_ = json.Unmarshal(goal.Payload, &payload)
			goalSummary = append(goalSummary, inheritedMap(map[string]any{
				"id": goal.ID, "state": goal.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["goals"] = goalSummary

		hints := readNodes(db.KindHint, relatedOverviewMaxHintsPerSource)
		hintSummary := make([]map[string]any, 0, len(hints))
		for _, hint := range hints {
			var payload map[string]any
			_ = json.Unmarshal(hint.Payload, &payload)
			hintSummary = append(hintSummary, inheritedMap(map[string]any{
				"id": hint.ID, "state": hint.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["hints"] = hintSummary

		facts := readNodes(db.KindFact, relatedOverviewMaxFactsPerSource)
		findings := readNodes(db.KindFinding, relatedOverviewMaxFindingsPerTask)
		intentNodes := readNodes(db.KindIntent, 300)
		visible := visibleNodeIDs(goals, hints, facts, findings, intentNodes)
		factFrom = restrictNodeOrigins(factFrom, visible)
		terminalIntent := make(map[int64]bool, len(intentNodes))
		for _, intent := range intentNodes {
			terminalIntent[intent.ID] = inheritedIntentSummaryState(intent.State)
		}
		item["facts_in_window"] = len(facts)
		item["findings_in_window"] = len(findings)
		if statsErr == nil && t.as == nil {
			item["facts"] = stats[db.KindFact]
			item["findings"] = stats[db.KindFinding]
			if stats[db.KindGoal] > len(goals) || stats[db.KindHint] > len(hints) ||
				stats[db.KindFact] > len(facts) || stats[db.KindFinding] > len(findings) {
				budget.truncated = true
			}
		}
		recentFindings := make([]map[string]any, 0, len(findings))
		for _, finding := range findings {
			entry := inheritedMap(compactFinding(finding), source.Task.TaskID)
			entry["summary"] = budget.take(entry["summary"], 400)
			recentFindings = append(recentFindings, entry)
		}
		item["recent_findings"] = recentFindings
		recentFacts := make([]map[string]any, 0, len(facts))
		for _, fact := range facts {
			if hidden(fact.ID) {
				continue // folded into this source's cold_digests — shown there (§2/§6.2)
			}
			m := inheritedMap(compactNode(fact), source.Task.TaskID)
			m["summary"] = budget.take(m["summary"], 400)
			if from := factFrom[fact.ID]; from > 0 && terminalIntent[from] {
				m["from_intent"] = from
			}
			var payload map[string]any
			if json.Unmarshal(fact.Payload, &payload) == nil {
				if confidence, ok := payload["confidence"].(string); ok && confidence != "" {
					m["confidence"] = confidence
				}
			}
			recentFacts = append(recentFacts, m)
		}
		item["recent_facts"] = recentFacts

		recentDoneRaw, terminalErr := ts.ToolTerminalIntents(relatedOverviewMaxIntentsPerTask)
		if terminalErr != nil {
			item["recent_intent_results_error"] = terminalErr.Error()
		}
		recentDoneRaw = t.filterAuthorizedNodesForStore(recentDoneRaw, ts, source.Task.TaskID)
		recentDone := recentDoneRaw[:0] // in-place filter: drop this source's folded intents (§2)
		for _, intent := range recentDoneRaw {
			if hidden(intent.ID) {
				continue
			}
			recentDone = append(recentDone, intent)
		}
		for _, intent := range recentDone {
			intent.Inherited = true
			intent.SourceTaskID = source.Task.TaskID
		}
		for id := range visibleNodeIDs(recentDone) {
			visible[id] = struct{}{}
		}
		parentsOf = restrictNodeRelations(parentsOf, visible)
		yieldsOf = restrictNodeRelations(yieldsOf, visible)
		intentResults := compactIntents(recentDone, parentsOf, yieldsOf)
		ids := make([]int64, 0, len(recentDone))
		for _, n := range recentDone {
			ids = append(ids, n.ID)
		}
		summaries, summaryErr := ts.ToolLatestTerminalSummaries(ids)
		if summaryErr != nil {
			item["intent_result_summaries_error"] = summaryErr.Error()
		}
		for i, intent := range recentDone {
			intentResults[i]["summary"] = budget.take(intentResults[i]["summary"], 400)
			if summary := summaries[intent.ID]; summary != "" {
				intentResults[i]["result_summary"] = budget.take(summary, 800)
			}
		}
		if terminalErr == nil {
			item["recent_intent_results"] = intentResults
		}
		// §2 cross-task: the source task's folded cold region, read-only. Members are
		// resolvable via expand_digest(id)/node_detail(id), which search source tasks.
		if cds := t.activeDigestBodies(ts, source.Task.TaskID); len(cds) > 0 {
			for _, cd := range cds {
				cd["inherited"] = true
				cd["source_task_id"] = source.Task.TaskID
			}
			item["cold_digests"] = cds
		}
		if statsErr == nil && t.as == nil {
			item["node_stats"] = stats
		}

		if t.as != nil {
			if scopeRows, err := t.as.ListTaskScope(source.Task.TaskID); err == nil && len(scopeRows) > 0 {
				scopeRows = t.authorizedScope(scopeRows)
				scopeCount := len(scopeRows)
				if len(scopeRows) > relatedOverviewMaxScopePerSource {
					scopeRows = scopeRows[:relatedOverviewMaxScopePerSource]
					budget.truncated = true
				}
				scope := make([]map[string]any, 0, len(scopeRows))
				for _, row := range scopeRows {
					entry := map[string]any{"kind": row.Kind, "source": budget.take(row.Source, 300)}
					switch {
					case row.Domain != "":
						entry["value"] = budget.take(row.Domain, 400)
					case row.Net != "":
						entry["value"] = budget.take(row.Net, 400)
					case row.Value != "":
						entry["value"] = budget.take(row.Value, 400)
					case row.CompanyID != nil:
						entry["company_id"] = *row.CompanyID
					}
					scope = append(scope, entry)
				}
				item["asset_scope"] = scope
				item["asset_scope_count"] = scopeCount
			}
			if coverage, err := t.as.TaskCoverage(source.Task.TaskID, source.Task.ExplorationID); err == nil {
				item["asset_coverage"] = map[string]any{
					"denominator": coverage.Denominator,
					"tested":      coverage.Tested,
					"pct":         coverage.Pct,
					"by_type":     coverage.ByType,
				}
			}
		}
		if budget.truncated {
			item["summary_truncated"] = true
		}
		if len(sourceErrors) > 0 {
			for key := range sourceErrors {
				switch key {
				case db.KindGoal:
					delete(item, "goals")
				case db.KindHint:
					delete(item, "hints")
				case db.KindFact:
					delete(item, "facts_in_window")
					delete(item, "recent_facts")
				case db.KindFinding:
					delete(item, "findings_in_window")
					delete(item, "recent_findings")
				case db.KindIntent:
					delete(item, "recent_intent_results")
				}
			}
			item["unavailable"] = sourceErrors
		}
		out = append(out, item)
	}
	return out, nil
}

func inheritedIntentSummaryState(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	return m
}

// assetValue distills an asset to its most identifying human-readable string
// (url / domain / ip[:port] / app / service name) so finding_list can show the
// affected asset's content inline instead of a bare id. Empty when nothing
// identifying is set (caller falls back to #id).
func assetValue(a *db.Asset) string {
	switch {
	case a.URL != "":
		if a.Method != "" {
			return a.Method + " " + a.URL // 接口：带上 HTTP 方法
		}
		return a.URL
	case a.Domain != "":
		if a.Port != nil {
			return fmt.Sprintf("%s:%d", a.Domain, *a.Port)
		}
		return a.Domain
	case a.IP != "":
		if a.Port != nil {
			return fmt.Sprintf("%s:%d", a.IP, *a.Port)
		}
		return a.IP
	case a.AppName != "":
		return a.AppName
	case a.ServiceName != "":
		return a.ServiceName
	}
	return ""
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	if vc, ok := p["vulnclass"]; ok && vc != nil && vc != "" {
		m["vulnclass"] = vc
	}
	if sv, ok := p["severity"]; ok && sv != nil && sv != "" {
		m["severity"] = sv
	}
	if id, ok := p["intent_id"]; ok {
		m["intent_id"] = id
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool { return t.nodeListTool(db.KindFinding) }
func (t *ToolSet) listFacts() actool.CoreTool    { return t.nodeListTool(db.KindFact) }

func (t *ToolSet) nodeListTool(kind string) actool.CoreTool {
	name, key := "list_facts", "facts"
	if kind == db.KindFinding {
		name, key = "list_findings", "findings"
	}
	description := "分页查询授权可见摘要。默认20条、最多100条；详情用 node_detail。q 仅搜索摘要，不搜索证据正文。"
	if kind == db.KindFinding {
		description += "查具体漏洞先用 asset_id、q、severity 筛选；未找到且 has_more=true 时继续翻页或调整筛选，不得断言不存在。翻页保留筛选条件，将 next_before 作为 before；改变筛选时清除 before。找到所需记录即可停止，无需遍历全部。查询失败报告‘查询失败’，预算不足报告‘尚未查完’。查完仅能说‘当前授权可见记录中，该筛选条件下未找到’，不能断言目标没有漏洞或证据正文无相关内容。"
	} else {
		description += "before 使用上一页 next_before，翻页保留筛选条件；失败不等于没有结果。"
	}
	return readTool(name, description,
		obj(map[string]any{"limit": intp("1..100，默认20"), "before": intp("上一页 next_before"), "q": str("摘要关键词"), "severity": str("漏洞严重等级，仅漏洞列表适用"), "asset_id": idp("按关联资产筛选")}),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var q struct {
				Limit    int    `json:"limit"`
				Before   int64  `json:"before"`
				Q        string `json:"q"`
				Severity string `json:"severity"`
				AssetID  int64  `json:"asset_id"`
			}
			if err := decodeToolInput(raw, &q); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if q.Limit == 0 {
				q.Limit = 20
			}
			if q.Limit < 1 || q.Limit > 100 || q.Before < 0 || q.AssetID < 0 {
				return actool.Errorf("无效分页参数"), nil
			}
			if q.Severity != "" {
				switch q.Severity {
				case "critical", "high", "medium", "low", "info":
				default:
					return actool.Errorf("无效 severity"), nil
				}
				if kind != db.KindFinding {
					return actool.Errorf("severity 仅用于漏洞列表"), nil
				}
			}
			if t.ts == nil {
				return actool.Errorf("缺少任务探索上下文"), nil
			}
			store := t.ts
			if t.workerExecution {
				store = store.WithWorkerRead()
			}
			page, err := store.ToolNodePage(ctx, kind, strings.TrimSpace(q.Q), q.Severity, q.AssetID, q.Before, q.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			more := len(page.Nodes) > q.Limit
			if more {
				page.Nodes = page.Nodes[:q.Limit]
			}
			rows := make([]map[string]any, 0, len(page.Nodes))
			if kind == db.KindFinding {
				if err := store.PopulateFindingTrafficIDs(page.Nodes); err != nil {
					return actool.Errorf(err.Error()), nil
				}
			}
			for _, n := range page.Nodes {
				row := compactFact(n)
				if kind == db.KindFinding {
					row = compactFinding(n)
					if n.FindingID > 0 {
						row["finding_id"], row["finding_node_id"], row["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
					}
					row["task_id"] = n.SourceTaskID
				}
				rows = append(rows, row)
			}
			rows, cut := budgetRows(rows)
			more = more || cut
			out := map[string]any{key: rows, "total": page.Total, "has_more": more, "truncated": cut}
			if more && len(rows) > 0 {
				out["next_before"] = rows[len(rows)-1]["id"]
				out["read_hint"] = "结果未完整；未找到目标请保留筛选条件，用 next_before 作为 before 续页。改变筛选请清除 before；未查完不能断言不存在。"
			}
			return jsonResult(out)
		})
}

// factSummaryMax caps a fact summary in list_facts output. Facts carry one-line
// conclusions, but nothing enforces brevity; a runaway summary must not bloat a
// whole page. Full text stays available via node_detail(id).
const factSummaryMax = 160

// compactFact is compactNode with the summary rune-capped for list_facts, so a
// page of facts stays bounded regardless of how long any single summary grew.
func compactFact(n *db.Node) map[string]any {
	m := compactNode(n)
	if s, ok := m["summary"].(string); ok && len([]rune(s)) > factSummaryMax {
		m["summary"] = string([]rune(s)[:factSummaryMax]) + "…"
		m["summary_truncated"] = true
	}
	return m
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return readTool("node_detail", "按 id 取本任务或直接关联任务的【探索图节点】完整内容。继承节点带 source_task_id/inherited=true 且只读。仅限 list_facts/list_findings/graph_overview 返回的探索节点 id；资产请用 list_assets。",
		obj(map[string]any{"id": idp("探索图节点 id(非资产 id)"), "field": str("延期字段的 JSON Pointer，默认整个 payload"), "index": intp("集合 next_index 续页"), "offset": intp("文本 next_offset 续读"), "max_chars": intp("默认8000，最大24000")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("缺少任务探索上下文"), nil
			}
			var a struct {
				ID    int64  `json:"id"`
				Field string `json:"field"`
				Index int    `json:"index"`
				detailWindow
			}
			if err := decodeToolInput(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := a.detailWindow.validate(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := a.ID
			if id <= 0 {
				return actool.Errorf("id 必填"), nil
			}
			n, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("未找到探索节点 %d。若你想查的是资产，请用 list_assets（资产与探索节点是不同的 id 空间，资产 id 不能传给 node_detail）。", id)), nil
			}
			authorized, authErr := t.nodeAuthorization(n)
			if authErr != nil {
				return actool.Errorf(authErr.Error()), nil
			}
			if !authorized {
				return actool.Errorf("该节点关联的任务资产未获授权或已被封禁"), nil
			}
			projection, err := projectDetail(n.Payload, a.detailWindow, a.Field, a.Index)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := map[string]any{"id": n.ID, "kind": n.Kind, "state": n.State, "payload": projection}
			if n.Inherited {
				inheritedMap(out, n.SourceTaskID)
			}
			if err := t.ts.PopulateFindingTrafficIDs([]*db.Node{n}); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n.FindingID > 0 {
				out["finding_id"], out["finding_node_id"], out["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
			}
			return jsonResult(out)
		})
}

// --- planner write tools ---

// intentItem 是 add_intent 批量/单条的一条探索方向。
type intentItem struct {
	Summary   string            `json:"summary"`
	AssetIDs  []json.RawMessage `json:"asset_ids"`
	ParentIDs []json.RawMessage `json:"parent_ids"`
	Priority  int               `json:"priority"`
}

// addOneIntent 创建一条意图节点并连上游血缘，返回 id。
// 约束：意图只能锚在已确认知识上——每个 parent_id 必须是已存在的 fact/finding
// 节点（不能挂在别的意图/目标/提示上）。顶层全新方向留空 parent_ids，兜底连 origin fact。
// 这样"每个意图都连到 fact 节点、且是发现驱动而非凭空规划"从创建路径上被强制。
func (t *ToolSet) addOneIntent(it intentItem) (int64, error) {
	id, _, err := t.addOneIntentResult(it)
	return id, err
}

// addOneIntentResult is addOneIntent plus the admission result. created=false
// means the same normalized direction is already active and no graph mutation was
// made; callers can surface that fact without treating idempotency as an error.
func (t *ToolSet) addOneIntentResult(it intentItem) (id int64, created bool, err error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, false, fmt.Errorf("summary 不能为空")
	}
	// 先校验锚点（建节点前，避免坏锚点留下孤儿意图）。
	parents := pidList(it.ParentIDs)
	parentNodes := make([]*db.Node, 0, len(parents))
	for _, pidv := range parents {
		n, err := t.ts.GetNodeWithSources(pidv)
		if err != nil || n == nil {
			return 0, false, fmt.Errorf("parent_id %d 不存在于本任务或直接关联任务：parent_ids 必须是已存在的【事实(fact)/发现(finding)】节点 id；顶层全新方向请留空 parent_ids", pidv)
		}
		if n.Kind != db.KindFact && n.Kind != db.KindFinding {
			return 0, false, fmt.Errorf("parent_id %d 是 %q 节点，不能作为意图锚点：意图只能锚在已确认的【事实(fact)/发现(finding)】上，不能挂在意图/目标/提示上；顶层全新方向请留空 parent_ids", pidv, n.Kind)
		}
		if !t.nodeAuthorized(n) {
			return 0, false, fmt.Errorf("parent_id %d 关联的任务资产未获授权或已被封禁", pidv)
		}
		parentNodes = append(parentNodes, n)
	}
	priority := it.Priority
	if priority == 0 {
		priority = 5
	}
	anchors := pidList(it.AssetIDs)
	seenAnchors := make(map[int64]bool, len(anchors))
	for _, assetID := range anchors {
		seenAnchors[assetID] = true
	}
	// A derived intent inherits its evidence's asset authorization even when the
	// model omits asset_ids. Besides closing a visibility gap, this guarantees a
	// later revoke/delete selects and immediately cancels the derived Worker.
	for _, parentNode := range parentNodes {
		lineageIDs, lineageErr := t.nodeLineageAssetIDs(parentNode)
		if lineageErr != nil {
			return 0, false, fmt.Errorf("读取父节点资产失败：%w", lineageErr)
		}
		for _, assetID := range lineageIDs {
			if !seenAnchors[assetID] {
				seenAnchors[assetID] = true
				anchors = append(anchors, assetID)
			}
		}
	}
	if t.as != nil && t.taskID > 0 {
		if err := t.as.ValidateTaskAssetsApproved(t.taskID, anchors); err != nil {
			return 0, false, fmt.Errorf("意图资产未获授权：%w", err)
		}
	}
	payload := map[string]any{"summary": it.Summary}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	id, created, err = t.ts.AddIntentDeduplicated(payload, priority, anchors, "planner")
	if err != nil {
		return 0, false, err
	}
	if !created {
		return id, false, nil
	}
	// upstream lineage: link each (validated) fact/finding parent → this intent, so
	// "multiple facts combine into one new intent" is expressible.
	for _, parent := range parents {
		_ = t.ts.Link(parent, db.RelDerivedFrom, id)
	}
	// a top-level intent (no explicit parent) connects to the origin fact, so every
	// intent still traces back to a fact node — at task start the only fact is the
	// origin, and the first intents derive from it.
	if len(parents) == 0 {
		if origin, _ := t.ts.OriginFactID(); origin > 0 {
			_ = t.ts.Link(origin, db.RelDerivedFrom, id)
		}
	}
	return id, true, nil
}

func (t *ToolSet) addIntent() actool.CoreTool {
	return writeTool("add_intent", "生成【探索方向】写入 frontier，并连入探索链路。意图是开放的探索方向，不是固定类型——用 summary 一句话自由描述要探索/验证/利用什么。\n"+
		"★优先批量：一轮筛出的多个新方向放进 intents 数组一次提交（最多 4 条，比逐条调用省往返）。返回 ids 数组，与 intents 等长同序（失败项 id=0，详情见 errors；已存在的活跃同方向见 duplicates）。单条则省略 intents 直接给顶层 summary。",
		obj(map[string]any{
			"intents":    map[string]any{"type": "array", "maxItems": 4, "description": "【优先用这个】要新增的探索方向数组，最多 4 条，按顺序处理。每个元素字段同下方顶层字段（summary/asset_ids/parent_ids/priority）。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"summary":    str("[单条] 一句话描述这个探索方向：做什么+为什么。已写清方向即可，不依赖资产 id。"),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "本方向要测试/攻击的【目标资产 id】（**尽量传**，0/1/多个；是 list_assets 返回的资产 id，不是探索节点 id）：这条探索方向针对哪些资产（站点/接口/参数/主机等）。只要方向围绕某些具体资产就务必传上——它是「这条探索打哪些目标」的结构化标记，用于覆盖去重、把意图连入资产链路。仅当纯全局侦察、确实没有具体目标资产时才留空。"},
			"parent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "上游锚点 id（可选，0/1/多个）：本方向由哪些【已确认的事实(fact)/发现(finding)】综合得出。**只能填已存在的 fact/finding 节点 id,不能填意图/目标/提示**——意图必须锚在已确认知识上,发现驱动而非凭空规划。多个事实共同产生一个新意图就传多个;顶层全新侦察方向请留空（会自动挂到任务起点 origin fact）。"},
			"priority":   intp("优先级 0-10，默认5"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Intents    []intentItem `json:"intents"`
				intentItem              // 单条模式：顶层 summary/asset_ids/parent_ids/priority
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Intents) > 0
			items := a.Intents
			if !batch {
				items = []intentItem{a.intentItem}
			}
			if len(items) > 4 {
				return actool.Errorf("一轮最多新增 4 条意图；请只保留最高价值且互不重复的方向"), nil
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			duplicates := map[string]int64{}
			createdAny := false
			for i, it := range items {
				id, created, err := t.addOneIntentResult(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				if created {
					createdAny = true
				} else {
					duplicates[strconv.Itoa(i)] = id
				}
			}

			// 人经主 agent 直投意图 → 若任务已 done（无 open 目标的 goalless 分支），把它
			// 拉回 running，worker 才能领这条意图执行。resumeTask 仅由主 agent 的 Chat 接入
			// (SetResumeTask)；planner 的 ToolSet 为 nil，故 planner 自己调 add_intent 时此段
			// no-op，不影响其正常产意图。意图节点已在上面建好(open)，复活时不会被误判抽干。
			if createdAny && t.resumeTask != nil {
				t.resumeTask()
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				if existingID, duplicate := duplicates["0"]; duplicate {
					return actool.Text(fmt.Sprintf("intent already active: %d", existingID)), nil
				}
				return actool.Text(fmt.Sprintf("intent created: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			if len(duplicates) > 0 {
				out["duplicates"] = duplicates
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listGoals() actool.CoreTool {
	return readTool("list_goals", "列出本任务的目标节点及其状态（open/met），用于判断是否达成。",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _ := t.ts.ListByKind(db.KindGoal, 100)
			g = t.filterAuthorizedNodesForStore(g, t.ts, t.taskID)
			return jsonResult(g)
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return writeTool("prove_goal", "当你判断某个发现/事实证明了某个目标达成时调用：把证据节点连到目标节点，并标记目标 met。",
		obj(map[string]any{
			"goal_id":     idp("目标节点 id"),
			"evidence_id": idp("证明它的发现/事实节点 id"),
			"reason":      str("为什么这个证据满足该目标"),
		}, "goal_id", "evidence_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID     json.RawMessage `json:"goal_id"`
				EvidenceID json.RawMessage `json:"evidence_id"`
				Reason     string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			goal, ev := pid(a.GoalID), pid(a.EvidenceID)
			if goal == 0 || ev == 0 {
				return actool.Errorf("goal_id 和 evidence_id 必填"), nil
			}
			goalNode, err := t.ts.GetNode(goal)
			if err != nil || goalNode == nil || goalNode.Kind != db.KindGoal {
				return actool.Errorf("goal_id 必须是本任务的目标节点（关联任务目标只读）"), nil
			}
			evidenceNode, err := t.ts.GetNodeWithSources(ev)
			if err != nil || evidenceNode == nil || (evidenceNode.Kind != db.KindFact && evidenceNode.Kind != db.KindFinding) {
				return actool.Errorf("evidence_id 必须是本任务或直接关联任务的事实/漏洞节点"), nil
			}
			if !t.nodeAuthorized(evidenceNode) {
				return actool.Errorf("evidence_id 关联的任务资产未获授权或已被封禁"), nil
			}
			_ = t.ts.Link(ev, db.RelProves, goal)
			_ = t.ts.SetNodeState(goal, "met")
			// 每标记一个目标 met，就检查本任务是否【所有目标】都已 met；若是，自动判定
			// 任务完成（置 GoalMet），无需再依赖模型显式调 goal_met。
			if goals, err := t.ts.ListByKind(db.KindGoal, 1000); err == nil && len(goals) > 0 {
				allMet := true
				for _, g := range goals {
					if g.State != "met" {
						allMet = false
						break
					}
				}
				if allMet {
					t.GoalMet = true
					t.Reason = fmt.Sprintf("所有 %d 个目标均已 met（最后由 goal %d 触发）", len(goals), goal)
					return actool.Text(fmt.Sprintf("goal %d marked met；本任务所有目标均已达成，任务自动判定完成", goal)), nil
				}
			}
			return actool.Text(fmt.Sprintf("goal %d marked met", goal)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return writeTool("goal_met", "【立即结束整个任务】——仅当你确认任务的【全部目标都已真正达成、整体收官】时才调（注意是任务【整体】完成；仅仅达成了其中某一个目标/某一个 flag/某一个漏洞【不算】——那种情况用 prove_goal 标记该目标即可）。⚠️它不是用来“结束本轮规划”的：本轮没有新意图要派、或在等 worker 产出，都【直接结束本轮即可，不要调本工具】（0 个意图是完全正常的）。正常判定优先用 prove_goal 逐个证明目标；goal_met 只是绕过逐个证明、直接从全局收官的手段。",
		obj(map[string]any{"reason": str("达成理由（必须是目标真正达成的证据，不能是“本轮无新方向”这类结束本轮的理由）")}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Reason string }
			_ = json.Unmarshal(in, &a)
			t.GoalMet = true
			t.Reason = a.Reason
			return actool.Text("acknowledged: goal marked met"), nil
		})
}

// --- worker write tools ---

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "记录确认的漏洞，用 evidence 提供命令输出、日志等可验证证据。任务上下文传当前 intent_id。返回的 finding_id 是独立漏洞记录 ID，finding_node_id 是探索节点 ID（第一行保留该节点编号）。", obj(map[string]any{
		"vulnclass": str("漏洞类别"), "name": str("漏洞名称"), "severity": str("critical|high|medium|low"), "summary": str("发现摘要"),
		"intent_id": idp("当前任务的意图 id"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "受影响资产 id"},
		"evidence":         str("证据/PoC 文本"),
		"evidence_hint_id": idp("可选：本任务中对应此漏洞的提示节点 ID，自动携带其结构化 traffic_refs；不能引用继承提示或其他漏洞的提示"),
		"traffic_refs": map[string]any{"type": "array", "description": "可选；HTTP/HTTPS 漏洞先检索并逐条核实请求/响应确实支持漏洞结论，再按复现顺序填写真实 ID。TCP 等非 HTTP 漏洞、未采集或找不到确切记录时省略或传 []，不阻止上报；可在 evidence 说明原因并提供其他可验证证据。不要猜测 ID、按域名/时间推定关联或仅为补包重复探测。用途 baseline 正常对照 / proof 漏洞证明 / verification 补充验证 / supporting 辅助证据。",
			"items": obj(map[string]any{"traffic_id": str("traffic_search 返回的真实流量 ID"), "role": map[string]any{"type": "string", "enum": []string{"baseline", "proof", "verification", "supporting"}}, "note": str("该流量如何支持漏洞结论")}, "traffic_id")},
	}, "vulnclass", "severity", "summary"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		var a struct {
			VulnClass, Name, Severity, Summary, Evidence string
			IntentID                                     json.RawMessage   `json:"intent_id"`
			AssetIDs                                     []json.RawMessage `json:"asset_ids"`
			TrafficRefs                                  []db.TrafficRef   `json:"traffic_refs"`
			EvidenceHintID                               json.RawMessage   `json:"evidence_hint_id"`
		}
		if err := json.Unmarshal(in, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.ts == nil {
			return actool.Errorf("report_finding 需要任务上下文；平台对话请通过 add_task_hint 向对应任务交接漏洞，并在提示中携带已有的 traffic_refs，由任务 Agent 登记。已登记漏洞可用 bind_finding_traffic 补绑。"), nil
		}
		// Auto-binding off: ignore the evidence params instead of rejecting the call.
		// stripTrafficParameters already removes them from the advertised schema, but
		// models routinely emit fields anyway — failing here would discard a confirmed
		// finding over a stray parameter. The success path below reports evidence_status
		// "not_bound" with the "已关闭，可在页面人工关联" note, which is what the caller needs.
		if !findingTrafficBindingEnabled() {
			a.TrafficRefs, a.EvidenceHintID = nil, nil
		}
		if len(a.EvidenceHintID) > 0 && pid(a.EvidenceHintID) <= 0 {
			return actool.Errorf("evidence_hint_id 必须为有效的提示节点 ID；无交接提示时省略"), nil
		}
		refs, err := t.findingRefsFromHint(pid(a.EvidenceHintID), a.TrafficRefs)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		input := db.RecordFindingInput{TaskID: t.taskID, ExplorationID: t.ts.ID(), IntentID: pid(a.IntentID), VulnClass: a.VulnClass, Name: a.Name, Severity: a.Severity, Summary: a.Summary, Evidence: a.Evidence, Worker: t.worker, AssetIDs: pidList(a.AssetIDs)}
		if len(input.AssetIDs) == 0 {
			input.AssetIDs = t.ownerAssetIDs()
		}
		if t.as != nil && t.taskID > 0 {
			if err := t.validateResultAssets(input.AssetIDs); err != nil {
				return actool.Errorf("漏洞资产未获授权：" + err.Error()), nil
			}
		}
		if t.workerExecution {
			if input.IntentID == 0 {
				input.IntentID = t.ownerNode
			}
			if input.IntentID != t.ownerNode {
				return actool.Errorf("Worker 只能向当前意图写回"), nil
			}
		}
		if input.IntentID > 0 {
			node, err := t.ts.GetNode(input.IntentID)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id 必须是本任务的意图（关联任务意图只读）"), nil
			}
			if !t.nodeAuthorized(node) {
				return actool.Errorf("intent_id 关联的任务资产未获授权或已被封禁"), nil
			}
		}
		var recorded *db.RecordedFinding
		if t.findingRecorder != nil {
			recorded, err = t.findingRecorder.Record(ctx, input, refs)
		} else if len(refs) > 0 {
			return actool.Errorf("流量证据存储不可用；未登记漏洞"), nil
		} else {
			recorded, err = t.ts.RecordFinding(ctx, input)
		}
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.as != nil && t.taskID > 0 {
			if err := t.markResultAssetsTested(input.AssetIDs); err != nil {
				return actool.Errorf("漏洞已保存，但测试状态更新失败：" + err.Error()), nil
			}
		}
		if t.notifyFinding != nil && (!t.workerExecution || t.as == nil || t.as.ValidateTaskAssetsApproved(t.taskID, input.AssetIDs) == nil) {
			iid := input.IntentID
			if iid <= 0 {
				iid = t.ownerNode
			}
			t.notifyFinding(iid, a.Summary)
		} else if t.notify != nil {
			t.notify()
		}
		t.writes.Findings++
		// Keep the first line's node-ID contract for existing reporter triggers.
		for i := range recorded.Traffic.Bindings {
			recorded.Traffic.Bindings[i].Snapshot.ReqHead = ""
			recorded.Traffic.Bindings[i].Snapshot.RespHead = ""
		}
		result := struct {
			*db.RecordedFinding
			EvidenceStatus string `json:"evidence_status"`
			EvidenceNote   string `json:"evidence_note,omitempty"`
		}{RecordedFinding: recorded, EvidenceStatus: "bound"}
		if len(recorded.Traffic.Bindings) == 0 {
			result.EvidenceStatus = "not_bound"
			result.EvidenceNote = "漏洞已保存，未绑定流量。TCP/无包情形可正常继续；若已有核实的 HTTP 流量，请用可用的 bind_finding_traffic 或漏洞页面补绑，再完成证据交接。不要重复创建漏洞。"
			if !findingTrafficBindingEnabled() {
				result.EvidenceNote = "漏洞已保存。Agent 自动绑定流量已关闭，可在页面人工关联流量。"
			}
		}
		raw, _ := json.Marshal(result)
		return actool.Text(fmt.Sprintf("finding recorded: %d\n%s", recorded.NodeID, raw)), nil
	})
}

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// factItem 是 record_fact 批量/单条的一条事实。
type factItem struct {
	Summary    string            `json:"summary"`
	Detail     string            `json:"detail"`
	Evidence   string            `json:"evidence"`   // 一行关键证据（命令+关键输出行），支撑结论、便于事后核对
	Confidence string            `json:"confidence"` // observed（直接看到）| inferred（据现象推断）
	IntentID   json.RawMessage   `json:"intent_id"`
	AssetIDs   []json.RawMessage `json:"asset_ids"`
}

// recordOneFact 写一条 fact 节点并连到意图（intent→yields→fact）。defaultIntent 为
// 批量时的默认意图（本条未给 intent_id 时用）。
func (t *ToolSet) recordOneFact(it factItem, defaultIntent int64) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary 不能为空")
	}
	payload := map[string]any{"summary": it.Summary}
	if it.Detail != "" {
		payload["detail"] = it.Detail
	}
	if e := strings.TrimSpace(it.Evidence); e != "" {
		payload["evidence"] = e
	}
	if c := strings.TrimSpace(it.Confidence); c != "" {
		payload["confidence"] = c
	}
	intent := pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	if t.workerExecution && intent != t.ownerNode {
		return 0, fmt.Errorf("Worker 只能向当前意图写回")
	}
	if intent > 0 {
		node, err := t.ts.GetNode(intent)
		if err != nil || node == nil || node.Kind != db.KindIntent {
			return 0, fmt.Errorf("intent_id 必须是本任务的意图（关联任务意图只读）")
		}
		if !t.nodeAuthorized(node) {
			return 0, fmt.Errorf("intent_id 关联的任务资产未获授权或已被封禁")
		}
	}
	anchors := pidList(it.AssetIDs)
	if len(anchors) == 0 {
		anchors = t.ownerAssetIDs()
	}
	if t.as != nil && t.taskID > 0 {
		if err := t.validateResultAssets(anchors); err != nil {
			return 0, fmt.Errorf("事实资产未获授权：%w", err)
		}
	}
	// a fact is its OWN node kind (distinct from a vuln finding).
	id, err := t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, anchors)
	if err != nil {
		return 0, err
	}
	if intent > 0 {
		_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> fact
	}
	if t.as != nil && t.taskID > 0 {
		if err := t.markResultAssetsTested(anchors); err != nil {
			return 0, err
		}
	}
	t.writes.Facts++
	return id, nil
}

// ownerAssetIDs returns assets anchored to the current worker intent and is
// used when a write tool omits an explicit asset_ids list.
func (t *ToolSet) ownerAssetIDs() []int64 {
	if t.ts == nil || t.ownerNode <= 0 {
		return nil
	}
	n, err := t.ts.GetNode(t.ownerNode)
	if err == nil && n != nil {
		var p struct {
			AssetIDs []int64 `json:"asset_ids"`
		}
		if json.Unmarshal(n.Payload, &p) == nil && len(p.AssetIDs) > 0 {
			return p.AssetIDs
		}
	}
	ids, _ := t.ts.AnchorAssetIDs(t.ownerNode)
	return ids
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return writeTool("record_fact", "把探索【事实/结论】写入探索图，连到产生它的意图（intent_id）。用于记录探索结果——包括指纹/枚举等【正向结论】，和'端口关闭'/'参数不可注入'/'未发现登录入口'等【否定结论】。\n"+
		"⚠️一次探索的多个观察要【汇总成一条事实】，不要拆成多条，可以合并成一条事实的就尽量用一条事实表示：summary=对本次结论的总结性一句话，detail=相关细节（可含多个具体项）。例：指纹意图→一条事实 {summary:'识别了 X 站点的技术栈与响应特征', detail:'nginx 1.25 / Vue3 / 200 / title=.. / body_len=..'}，而不是状态码、指纹、标题各记一条。一条意图通常只产出一条事实，拆太碎会让图谱无限膨胀。\n"+
		"★facts 数组用于一次写多条【彼此不同】的结论（每条可省略 intent_id，默认用顶层 intent_id）。返回 ids 数组，与 facts 等长同序。\n"+
		"⚠️只写你在工具输出里【真实看到】的结论，不要脑补。evidence 与 confidence 用来防止不准确的结论污染图谱：\n"+
		"  · evidence=支撑本结论的【一行】关键证据（命令+最能证明的那一两行输出），**务必简洁**——细节已在 detail，这里不要再粘大段输出。\n"+
		"  · confidence=observed（输出里直接看到）| inferred（据现象推断）。\n"+
		"  · **否定类结论**（不可注入/端口关闭/未发现入口等）只写\"观察 + 试探性读法\"——陈述你实际看到什么，方向是否放弃由规划者综合全局定；务必给 evidence，手段没穷尽或证据弱（含只探一次、看起来像）标 inferred，确已穷尽且直接看到才标 observed。",
		obj(map[string]any{
			"facts":      map[string]any{"type": "array", "description": "【有多条不同结论时用】事实数组，元素字段同下方顶层字段（summary/detail/evidence/confidence/intent_id/asset_ids）；省略 intent_id 则用顶层 intent_id。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"summary":    str("对本次探索结论的【总结性一句话】（是对 detail 的概括）"),
			"intent_id":  idp("产生本事实的意图 id（你领到的意图；批量时作为各条默认）"),
			"detail":     str("本事实的相关细节：把这次探索的多个观察事实都写进这里"),
			"evidence":   str("【一行】关键证据：命令 + 最能证明结论的那一两行输出。务必简洁，不要粘大段输出（细节放 detail）。"),
			"confidence": str("observed（输出里直接看到）| inferred（据现象推断）。否定结论务必如实标注。"),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "相关资产 id（可选，0/1/多个）：该事实涉及哪些资产"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Facts    []factItem `json:"facts"`
				factItem            // 单条模式 + 批量默认 intent_id
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Facts) > 0
			items := a.Facts
			if !batch {
				items = []factItem{a.factItem}
			}
			defaultIntent := pid(a.factItem.IntentID) // 顶层 intent_id = 批量默认

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.recordOneFact(it, defaultIntent)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("fact recorded: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type hintItem struct {
	Text        string            `json:"text"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
	TrafficRefs []db.TrafficRef   `json:"traffic_refs"`
}

// addOneHint 挂一条 hint 节点(active/human)到探索图,可锚定资产,返回 id。
func (t *ToolSet) addOneHint(it hintItem) (int64, error) {
	if len(it.TrafficRefs) > 0 && !findingTrafficBindingEnabled() {
		return 0, fmt.Errorf("Agent 自动绑定流量已关闭，未保存携带 traffic_refs 的提示；可在系统设置开启，或仅交接文字")
	}
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("text 不能为空")
	}
	var anchors []int64
	for _, raw := range it.AssetIDs {
		if tid := pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	if t.as != nil && t.taskID > 0 {
		if err := t.as.ValidateTaskAssetsApproved(t.taskID, anchors); err != nil {
			return 0, fmt.Errorf("提示资产未获授权：%w", err)
		}
	}
	refs, err := db.NormalizeTrafficRefs(it.TrafficRefs)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{"text": it.Text}
	if len(refs) > 0 {
		payload["traffic_refs"] = refs
	}
	id, err := t.ts.AddNode(db.KindHint, payload, 0, "active", "human", anchors)
	if err == nil && t.notify != nil {
		t.notify() // wake the planner so the new hint is read promptly (debounced)
	}
	return id, err
}

type goalItem struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass"`
}

// addOneGoal 挂一条 goal 节点(open)到探索图:连到任务根(origin fact,rel spawns)。
// origin 取 t.worker(缺省 system):goals 拆解器写入的记 "goals"、主 agent 运行时记
// "human"。唤醒 planner 由 setGoals 在整批写完后统一做(见下),这里只负责落库。
func (t *ToolSet) addOneGoal(it goalItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text 不能为空")
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(it.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	origin := t.worker
	if origin == "" {
		origin = "system"
	}
	id, err := t.ts.AddNode(db.KindGoal, payload, 0, "open", origin, nil)
	if err != nil {
		return 0, err
	}
	if of, _ := t.ts.OriginFactID(); of > 0 && id > 0 {
		_ = t.ts.Link(of, db.RelSpawns, id) // goals descend from the task root (origin fact)
	}
	return id, nil
}

// setGoals 给【本任务】新增探索目标(goal 节点)。既是目标拆解器的提交工具,也是主
// agent 运行时补目标的工具——同一个受管工具,可在 web 端改描述/schema、按 agent 绑定。
func (t *ToolSet) setGoals() actool.CoreTool {
	return writeTool("set_goals",
		"给【本任务】新增探索目标(goal)。目标=最终可交付/可核验的结果,不是攻击步骤或侦察动作。\n"+
			"★优先批量:多个目标放进 goals 数组一次提交,返回 ids 与之等长同序(失败项 id=0,详情见 errors)。单条则省略 goals 直接给顶层 text。\n"+
			"vulnclass 可选:对应漏洞类(如 SQLi/IDOR),业务逻辑类目标留空。目标是否达成由系统判定标记 met,本工具只负责新增。",
		obj(map[string]any{
			"goals":     map[string]any{"type": "array", "description": "【优先用这个】要新增的目标数组,按顺序处理。每个元素:text(必填,一个独立可验证的最终目标)+ vulnclass(可选)。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"text":      str("[单条] 一个独立可验证的最终目标"),
			"vulnclass": str("[单条] 对应漏洞类(若明确),如 SQLi/IDOR;业务逻辑目标可留空"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_goals 未启用: ExplorationStore 未初始化"), nil
			}
			var a struct {
				Goals    []goalItem `json:"goals"`
				goalItem            // 单条模式:顶层 text/vulnclass
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Goals) > 0
			items := a.Goals
			if !batch {
				items = []goalItem{a.goalItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneGoal(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// 唤醒 planner(整批一次)。优先 notifyGoal:一次 set_goals 记一条「人新增了
				// N 个目标:…」触发,不逐条刷屏;拆解器/worker 无此回调 → 退回纯 notify(拆解器
				// round-0 连 notify 也没接,即无操作,因为此时 planner 尚未启动)。
				switch {
				case t.notifyGoal != nil:
					t.notifyGoal(addedTexts)
				case t.notify != nil:
					t.notify()
				}
				// 主 agent 运行时新增目标 → 把已完成/暂停的任务拉回 running 继续跑(终态门会
				// 吞掉普通 notify,必须显式复活)。仅 mainagent 接了此回调;拆解器/worker 为 nil。
				if t.resumeTask != nil {
					t.resumeTask()
				}
			}

			if !batch { // 单条:保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("goal added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type constraintItem struct {
	Text string `json:"text"`
	Type string `json:"type"` // allow | deny
}

// addOneConstraint 落一条操作约束到 task_constraints。origin 取 t.worker(缺省 system):
// 拆解器写 "goals"、主 agent 写 "human"。
func (t *ToolSet) addOneConstraint(it constraintItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text 不能为空")
	}
	kind := strings.TrimSpace(strings.ToLower(it.Type))
	if kind == "" {
		kind = "deny" // 默认按禁止处理:未标注类型时更保守
	}
	if kind != "allow" && kind != "deny" {
		return 0, fmt.Errorf("type 必须是 allow 或 deny")
	}
	return t.ts.AddConstraint(kind, text, t.worker)
}

// setConstraints 给【本任务】新增操作约束(allow=允许做什么 / deny=禁止做什么)。既是目标
// 拆解器 round-0 抽约束的提交工具,也是主 agent 运行时补约束的工具——同一受管工具,可在 web
// 端改描述/schema、按 agent 绑定。约束会被注入 planner/worker 的系统提示以约束探索边界。
func (t *ToolSet) setConstraints() actool.CoreTool {
	return writeTool("set_constraints",
		"给【本任务】新增操作约束,用来框定探索边界:type=allow(允许做的操作)或 deny(禁止做的操作)。\n"+
			"约束=对『可以/不可以做哪些操作』的规定(如『仅测当前端口,不扫其他端口』『禁止对生产库做写操作』『只允许被动侦察』),不是目标、也不是攻击步骤。\n"+
			"★优先批量:多条放进 constraints 数组一次提交,返回 ids 与之等长同序(失败项 id=0,详情见 errors)。单条则省略 constraints 直接给顶层 text/type。\n"+
			"只登记任务目标/描述里【明确写出】的约束,不要臆造;拿不准类型时用 deny(更保守)。",
		obj(map[string]any{
			"constraints": map[string]any{"type": "array", "description": "【优先用这个】要新增的约束数组,按顺序处理。每个元素:text(必填,一条约束)+ type(allow|deny)。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"text":        str("[单条] 一条操作约束的内容"),
			"type":        str("[单条] allow(允许)或 deny(禁止);缺省按 deny 处理"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_constraints 未启用: ExplorationStore 未初始化"), nil
			}
			var a struct {
				Constraints    []constraintItem `json:"constraints"`
				constraintItem                  // 单条模式:顶层 text/type
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Constraints) > 0
			items := a.Constraints
			if !batch {
				items = []constraintItem{a.constraintItem}
			}
			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneConstraint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}
			if !batch { // 单条:保持简单返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("constraint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) addHint() actool.CoreTool {
	return writeTool("add_hint", "把人类/主 agent 的战略提示挂到探索图，规划者下次生成意图时会读到它。\n"+
		"★优先批量：多条提示放进 hints 数组一次提交（比逐条调用省往返）。返回 ids 数组，与 hints 等长同序（失败项 id=0，详情见 errors）。单条则省略 hints 直接给顶层 text。",
		obj(map[string]any{
			"hints":        map[string]any{"type": "array", "description": "【优先用这个】要新增的提示数组，按顺序处理。每个元素字段同下方顶层字段（text/asset_ids/traffic_refs）。返回 ids 与本数组等长、同序。", "items": obj(map[string]any{"text": str("提示内容"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": HintTrafficSchema()})},
			"text":         str("[单条] 提示内容，如'重点挖认证后接口'"),
			"traffic_refs": HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "锚定的资产 id（可选，0/1/多个）"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Hints    []hintItem `json:"hints"`
				hintItem            // 单条模式：顶层 text/asset_ids
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Hints) > 0
			items := a.Hints
			if !batch {
				items = []hintItem{a.hintItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneHint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("hint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return writeTool("kill_work", "终止一条正在运行的意图(work)。用于叫停跑偏/无意义的探索；被终止的意图标记为 stopped，不再自动重领。先用 get_worker_output 看看它在干嘛再决定。",
		obj(map[string]any{"intent_id": idp("要终止的意图 id（= work 句柄）")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("kill_work 当前不可用"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id 必须是本任务的意图（关联任务意图只读）"), nil
			}
			if !t.nodeAuthorized(node) {
				return actool.Errorf("该意图关联的任务资产未获授权或已被封禁"), nil
			}
			if err := t.killWork(id); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("已向意图 %d 的 work 发送终止信号", id)), nil
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// nudges ("停做 X、聚焦 Y"); if the whole direction is wrong use kill_work + a new intent.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return writeTool("steer_work", "给一条正在运行的意图(work)实时注入纠偏指令，不打断它、不丢已有进展：worker 会在下一步动作前收到你的指令并据此调整。用于'别再走 X、聚焦 Y'这类【意图内】纠偏；若方向整个错了应改用 kill_work 再下新意图。建议先用 get_worker_output 看它在干嘛。",
		obj(map[string]any{
			"intent_id": idp("要纠偏的意图 id（= work 句柄）"),
			"message":   str("给 worker 的纠偏指令，明确让它停止什么、转向什么"),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("steer_work 当前不可用"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message  string          `json:"message"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id 必须是本任务的意图（关联任务意图只读）"), nil
			}
			if !t.nodeAuthorized(node) {
				return actool.Errorf("该意图关联的任务资产未获授权或已被封禁"), nil
			}
			if strings.TrimSpace(a.Message) == "" {
				return actool.Errorf("message 必填"), nil
			}
			if err := t.steerWork(id, a.Message); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("已向意图 %d 的 work 注入纠偏指令（下一步生效）", id)), nil
		})
}

// getWorkerOutput returns a work's final (or截至中止时的) conclusion text by intent id.
func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return readTool("get_worker_output", "读取最新 result，没有 result 时返回最新 text。state 表示意图当前状态；长正文使用 next_offset 续读。",
		obj(map[string]any{"intent_id": idp("意图 ID"), "offset": intp("正文字符偏移，默认0"), "max_chars": intp("正文字符数，默认8000，最大24000")}, "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID int64 `json:"intent_id"`
				detailWindow
			}
			if err := decodeToolInput(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.IntentID <= 0 {
				return actool.Errorf("intent_id 必须为正整数"), nil
			}
			if err := a.detailWindow.validate(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if t.ts == nil {
				return actool.Errorf("缺少任务探索上下文"), nil
			}
			n, err := t.ts.GetNodeWithSources(a.IntentID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil || n.Kind != db.KindIntent {
				return actool.Errorf("intent_id 不属于本任务或直接关联任务"), nil
			}
			authorized, authErr := t.nodeAuthorization(n)
			if authErr != nil {
				return actool.Errorf(authErr.Error()), nil
			}
			if !authorized {
				return actool.Errorf("该 work 关联的任务资产未获授权或已被封禁"), nil
			}
			pick, err := t.ts.LatestWorkerOutput(ctx, a.IntentID, a.Offset, a.MaxChars)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := map[string]any{"intent_id": a.IntentID, "state": n.State, "has_output": pick != nil}
			if n.Inherited {
				inheritedMap(out, n.SourceTaskID)
			}
			if pick != nil {
				total := pick.DetailChars
				next := min(a.Offset, total) + len([]rune(pick.Detail))
				out["final_text"] = pick.Detail
				out["total_chars"] = total
				out["offset"] = a.Offset
				out["truncated"] = next < total
				if next < total {
					out["next_offset"] = next
				}
				out["kind"] = pick.Kind
				out["worker_name"] = pick.Worker
				out["is_error"] = pick.IsError
				out["terminated"] = n.State == "stopped" || n.State == "blocked" || n.State == "exhausted"
			}
			return jsonResult(out)
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps := make([]map[string]any, 0, len(acts))
	for i := range acts {
		step := map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": firstLine(acts[i].Tool, 100),
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		}
		if acts[i].Inherited {
			inheritedMap(step, acts[i].SourceTaskID)
		}
		steps = append(steps, step)
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return readTool("get_worker_trace", "读取意图执行轨迹摘要，默认20，最大100，before续页；step_ids显式读取至多5个步骤正文，与q/before互斥。正文默认8000、最大24000字符，offset续读。不包含思考。",
		obj(map[string]any{"intent_id": idp("意图 ID"), "q": str("摘要/正文关键词"), "before": intp("摘要 next_before"), "limit": intp("摘要默认20，最大100"), "step_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "maxItems": 5}, "offset": intp("正文 next_offset"), "max_chars": intp("正文默认8000，最大24000")}, "intent_id"),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var in struct {
				IntentID int64   `json:"intent_id"`
				Q        string  `json:"q"`
				Before   int64   `json:"before"`
				Limit    int     `json:"limit"`
				StepIDs  []int64 `json:"step_ids"`
				detailWindow
			}
			if err := decodeToolInput(raw, &in); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := in.detailWindow.validate(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.IntentID <= 0 || in.Before < 0 || in.Limit < 1 || in.Limit > 100 || len(in.StepIDs) > 5 {
				return actool.Errorf("无效 ID 或分页范围"), nil
			}
			for _, id := range in.StepIDs {
				if id <= 0 {
					return actool.Errorf("step_ids 必须为正整数"), nil
				}
			}
			if len(in.StepIDs) > 0 && (in.Q != "" || in.Before != 0) {
				return actool.Errorf("step_ids 与 q/before 互斥"), nil
			}
			if len(in.StepIDs) == 0 && in.Offset != 0 {
				return actool.Errorf("offset 仅用于步骤正文"), nil
			}
			if t.ts == nil {
				return actool.Errorf("缺少任务探索上下文"), nil
			}
			n, err := t.ts.GetNodeWithSources(in.IntentID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil || n.Kind != db.KindIntent {
				return actool.Errorf("意图不属于本任务或直接关联任务"), nil
			}
			authorized, authErr := t.nodeAuthorization(n)
			if authErr != nil {
				return actool.Errorf(authErr.Error()), nil
			}
			if !authorized {
				return actool.Errorf("该 work 关联的任务资产未获授权或已被封禁"), nil
			}
			out := map[string]any{"intent_id": in.IntentID, "state": n.State}
			if n.Inherited {
				inheritedMap(out, n.SourceTaskID)
			}
			if len(in.StepIDs) > 0 {
				acts, err := t.ts.ActivityByIDsWithSources(in.StepIDs)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				steps := []map[string]any{}
				returned := []int64{}
				for _, a := range acts {
					if a.NodeID == nil || *a.NodeID != in.IntentID || a.Inherited != n.Inherited || (a.Inherited && a.SourceTaskID != n.SourceTaskID) {
						continue
					}
					row := map[string]any{"step_id": a.ID, "kind": a.Kind, "tool": a.Tool, "is_error": a.IsError}
					window := in.detailWindow
					budget := (toolListBudget - 512) / len(in.StepIDs)
					for {
						part, total, next := textWindow(a.Detail, window)
						row["detail"] = part
						row["total_chars"] = total
						row["truncated"] = next < total
						row["offset"] = in.Offset
						if next < total {
							row["next_offset"] = next
						} else {
							delete(row, "next_offset")
						}
						data, _ := json.Marshal(row)
						if len([]rune(string(data))) <= budget {
							break
						}
						if window.MaxChars <= 1 {
							return actool.Errorf("步骤元数据超出预算"), nil
						}
						window.MaxChars = max(1, window.MaxChars/2)
					}
					if a.Inherited {
						inheritedMap(row, a.SourceTaskID)
					}
					steps = append(steps, row)
					returned = append(returned, a.ID)
				}
				out["steps"] = steps
				out["returned_step_ids"] = returned
			} else {
				acts, err := t.ts.ToolWorkerTracePage(ctx, in.IntentID, strings.TrimSpace(in.Q), in.Before, in.Limit)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				more := len(acts) > in.Limit
				if more {
					acts = acts[:in.Limit]
				}
				out["steps"] = traceSteps(acts)
				out["has_more"] = more
				if more && len(acts) > 0 {
					out["next_before"] = acts[len(acts)-1].ID
				}
			}
			return jsonResult(out)
		})
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return readTool("search_all_worker_traces", "按关键词搜索其他 Worker 的授权可见轨迹摘要，不包含思考；详情用 get_worker_trace。默认20，最大100。",
		obj(map[string]any{"q": str("正文或摘要关键词"), "limit": intp("默认20，最大100"), "before": intp("next_before 续页")}, "q"),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("缺少任务探索上下文"), nil
			}
			var in struct {
				Q      string `json:"q"`
				Limit  int    `json:"limit"`
				Before int64  `json:"before"`
			}
			if err := decodeToolInput(raw, &in); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			in.Q = strings.TrimSpace(in.Q)
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.Q == "" || in.Limit < 1 || in.Limit > 100 || in.Before < 0 {
				return actool.Errorf("关键词或分页参数无效"), nil
			}
			acts, err := t.ts.ToolSearchWorkerTraces(ctx, t.ownerNode, in.Q, in.Before, in.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			more := len(acts) > in.Limit
			if more {
				acts = acts[:in.Limit]
			}
			rows := []map[string]any{}
			for _, a := range acts {
				row := map[string]any{"intent_id": *a.NodeID, "step_id": a.ID, "worker": a.Worker, "kind": a.Kind, "tool": a.Tool, "is_error": a.IsError, "summary": a.Summary}
				if a.Inherited {
					inheritedMap(row, a.SourceTaskID)
				}
				rows = append(rows, row)
			}
			rows, cut := budgetRows(rows)
			more = more || cut
			out := map[string]any{"query": in.Q, "hits": rows, "has_more": more, "truncated": cut}
			if more && len(rows) > 0 {
				out["next_before"] = rows[len(rows)-1]["step_id"]
			}
			return jsonResult(out)
		})
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return readTool("list_worker_traces", "分页查询已执行意图摘要，用于复用其他 Worker 观察；详情使用 get_worker_trace。默认20，最大100。",
		obj(map[string]any{"q": str("摘要关键词"), "limit": intp("默认20，最大100"), "before": intp("next_before 续页")}),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("缺少任务探索上下文"), nil
			}
			var in struct {
				Q      string `json:"q"`
				Limit  int    `json:"limit"`
				Before int64  `json:"before"`
			}
			if err := decodeToolInput(raw, &in); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.Limit < 1 || in.Limit > 100 || in.Before < 0 {
				return actool.Errorf("无效分页参数"), nil
			}
			page, err := t.ts.ToolNodePage(ctx, db.KindIntent, strings.TrimSpace(in.Q), "", 0, in.Before, in.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			more := len(page.Nodes) > in.Limit
			if more {
				page.Nodes = page.Nodes[:in.Limit]
			}
			rows := []map[string]any{}
			for _, n := range page.Nodes {
				row := compactFact(n)
				row["intent_id"] = n.ID
				delete(row, "id")
				rows = append(rows, row)
			}
			rows, cut := budgetRows(rows)
			more = more || cut
			out := map[string]any{"works": rows, "total": page.Total, "has_more": more, "truncated": cut}
			if more && len(rows) > 0 {
				out["next_before"] = rows[len(rows)-1]["intent_id"]
			}
			return jsonResult(out)
		})
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.listTaskAssets(),
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		// cold-digest §6.1: restore folded cold nodes (digest body → members → detail).
		t.expandDigest(), t.expandIndex(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.listGoals(), t.addIntent(), t.proveGoal(), t.goalMet(),
		t.killWorkTool(), t.steerWorkTool(),
		// report_finding：规划态势研判时若自身已确证漏洞，可直接登记（与 worker 同工具）。
		t.addFinding(),
		// list_companies：查看企业列表 + scope + 资产数（拿 company_id / 理解归属范围）。
		t.listCompanies(),
		// list_assets：规划时按 DSL 检索全资产库（配合 list_untested_assets 的"范围内未测"视角，
		// 补上"按域名/指纹/端口/状态码等条件在整库里查"的能力）。
		t.listAssets(),
		// add_company_scope：规划时可把域名/IP/CIDR/ICP/关键词纳入某公司的资产范围（自动认领命中资产）。
		t.addCompanyScope(),
		// add_task_scope：主动把整根域/整公司/某子域/IP 纳入本任务测试范围(覆盖度分母)。
		t.addTaskScope(),
		// list_untested_assets：按需查本任务范围内未测资产(类型+分页)，自行决定补测。
		t.listUntestedAssets(),
	}
}
