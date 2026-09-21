import type { Activity } from "./types";

export type Group =
  | { type: "user"; key: number; step: Activity; intent?: boolean }
  | { type: "answer"; key: number; step: Activity }
  | { type: "round"; key: number; label: string }
  | { type: "tool"; key: number; worker: string; use?: Activity; result?: Activity }
  | { type: "msg"; key: number; worker: string; steps: Activity[] }
  | { type: "intercept"; key: number; step: Activity };

export function groupSteps(steps: Activity[], chat: boolean): Group[] {
  const out: Group[] = [];
  const byToolId = new Map<string, Extract<Group, { type: "tool" }>>();
  for (const s of steps) {
    if (s.kind === "usage") continue; // live token-usage marker — not a rendered step
    if (s.kind === "round") {
      out.push({ type: "round", key: s.seq, label: s.summary || "新一轮" }); // planner round boundary
      continue;
    }
    if (s.kind === "intercept_request") {
      out.push({ type: "intercept", key: s.seq, step: s });
      continue;
    }
    if (s.kind === "user" || s.kind === "intent") {
      // human turn OR the LLM-generated intent leading a worker session — both are
      // right-aligned bubbles; `intent` swaps the avatar to a non-human icon.
      out.push({ type: "user", key: s.seq, step: s, intent: s.kind === "intent" });
      continue;
    }
    // In a chat (main agent) the assistant's text/result IS the answer — render it
    // full (markdown), never collapsed. thinking still folds into a compact block.
    if (s.kind === "result" || (chat && s.kind === "text")) {
      out.push({ type: "answer", key: s.seq, step: s });
      continue;
    }
    if (s.kind === "tool_use") {
      const g: Extract<Group, { type: "tool" }> = { type: "tool", key: s.seq, worker: s.worker, use: s };
      if (s.tool_use_id) byToolId.set(s.tool_use_id, g);
      out.push(g);
      continue;
    }
    if (s.kind === "tool_result") {
      // bind to its tool_use by id (NOT adjacency — tools can run in parallel)
      const g = s.tool_use_id ? byToolId.get(s.tool_use_id) : undefined;
      if (g && !g.result) g.result = s;
      else out.push({ type: "tool", key: s.seq, worker: s.worker, result: s }); // orphan result
      continue;
    }
    const last = out[out.length - 1];
    if (last && last.type === "msg" && last.worker === s.worker) last.steps.push(s);
    else out.push({ type: "msg", key: s.seq, worker: s.worker, steps: [s] });
  }
  return out;
}

export type ProcessGroup = { type: "process"; key: number; worker: string; groups: Group[] };
export function groupProcesses(groups: Group[]): (Group | ProcessGroup)[] {
  const out: (Group | ProcessGroup)[] = [];
  for (const group of groups) {
    const process =
      group.type === "tool"
        ? !group.use?.is_error && !group.result?.is_error
        : group.type === "msg" && group.steps.every((s) => s.kind === "thinking" && !s.is_error);
    if (!process || !("worker" in group)) {
      out.push(group);
      continue;
    }
    const last = out[out.length - 1];
    if (last?.type === "process" && last.worker === group.worker) last.groups.push(group);
    else out.push({ type: "process", key: group.key, worker: group.worker, groups: [group] });
  }
  return out;
}
export function processContainsActivity(group: ProcessGroup, seq?: number): boolean {
  return (
    seq != null &&
    group.groups.some((g) =>
      g.type === "tool"
        ? g.use?.seq === seq || g.result?.seq === seq
        : g.type === "msg" && g.steps.some((s) => s.seq === seq),
    )
  );
}
