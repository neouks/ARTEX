import type { ToolCall, ToolCallPage, ToolCallText } from "../tool-calls";
import type { Activity, Tool } from "../types";

// Current code-owned names used by these fixtures. Obsolete catalogue examples
// such as `bash` / `upsert_asset` are not silently treated as current built-ins.
const fixtureBuiltins = new Set([
  "Bash",
  "Read",
  "add_intent",
  "list_findings",
  "node_detail",
  "prove_goal",
  "report_finding",
  "ExecuteExtraTool",
  "SearchExtraTools",
]);

// Mock mirrors the persisted activity contract, not the aggregate usage ledger.
export function mockToolCalls(
  events: Activity[],
  catalog: Tool[],
  params: URLSearchParams,
  scope: string,
  running = false,
  detailID?: number,
): ToolCallPage | ToolCallText {
  const integer = (key: string, fallback: number, min: number, max: number) => {
    const raw = params.get(key);
    const n = raw == null ? fallback : Number(raw);
    if (!Number.isSafeInteger(n) || n < min || n > max) throw new Error(`无效 ${key}`);
    return n;
  };
  if (detailID != null) {
    const row = events.find((a) => a.seq === detailID && ["tool_use", "tool_result"].includes(a.kind));
    if (!row) throw new Error("record not in session");
    const offset = integer("offset", 0, 0, Number.MAX_SAFE_INTEGER);
    const limit = integer("limit", 8000, 1, 24000);
    const body = Array.from(row.detail || row.summary || "");
    if (offset > body.length) throw new Error("offset exceeds detail length");
    const next = Math.min(offset + limit, body.length);
    return {
      text: body.slice(offset, next).join(""),
      offset,
      next_offset: next,
      total: body.length,
      has_more: next < body.length,
    };
  }
  const limit = integer("limit", 20, 1, 50);
  const q = params.get("q")?.trim() ?? "",
    type = params.get("type") ?? "",
    status = params.get("status") ?? "";
  if (
    !["", "builtin", "custom", "mcp", "unknown"].includes(type) ||
    !["", "success", "failed", "running", "missing"].includes(status)
  )
    throw new Error("无效筛选");
  const snapshot_cursor = Math.max(0, ...events.map((a) => a.seq));
  let snapshot = snapshot_cursor,
    before = 0;
  const binding = JSON.stringify([scope, q, type, status]);
  const raw = params.get("cursor");
  if (raw) {
    try {
      const c = JSON.parse(decodeURIComponent(raw));
      if (
        c.binding !== binding ||
        !Number.isSafeInteger(c.before) ||
        c.before <= 0 ||
        !Number.isSafeInteger(c.snapshot) ||
        c.snapshot < c.before
      )
        throw new Error();
      before = c.before;
      snapshot = c.snapshot;
    } catch {
      throw new Error("无效或不匹配的分页游标");
    }
  }
  const calls = new Map<number, ToolCall>();
  const uses = new Map<string, number>();
  const epochs = new Map<string, number>();
  const terminal = Math.max(0, ...events.filter((a) => ["result", "round", "user"].includes(a.kind)).map((a) => a.seq));
  for (const a of [...events].sort((a, b) => a.seq - b.seq)) {
    const lane = JSON.stringify([a.worker, a.intent_id ?? "", a.main_seg ?? 0]);
    if (["result", "round", "user"].includes(a.kind)) {
      epochs.set(lane, a.seq);
      continue;
    }
    if (a.kind !== "tool_use" && a.kind !== "tool_result") continue;
    const key = JSON.stringify([lane, epochs.get(lane) ?? 0, a.tool_use_id]);
    if (a.kind === "tool_use" && a.tool_use_id) uses.set(key, a.seq);
    const anchor = a.tool_use_id ? (uses.get(key) ?? a.seq) : a.seq;
    const known = catalog.find((t) => t.key === a.tool);
    let kind: ToolCall["type"] = "unknown";
    if (fixtureBuiltins.has(a.tool ?? "")) kind = "builtin";
    else if (known?.system === false) kind = "custom";
    else if (/^mcp__[^_].*__.+$/.test(a.tool ?? "")) kind = "mcp";
    const call: ToolCall = calls.get(anchor) ?? {
      id: anchor,
      name: a.tool ?? "",
      created_at: a.ts,
      tool_use_id: a.tool_use_id ?? "",
      use_id: null,
      result_id: null,
      type: kind,
      status: running && anchor > terminal ? "running" : "missing",
    };
    if (a.kind === "tool_use") call.use_id = a.seq;
    else {
      call.result_id = a.seq;
      call.status = a.is_error ? "failed" : "success";
    }
    calls.set(anchor, call);
  }
  const filtered = [...calls.values()]
    .filter(
      (c) =>
        c.id <= snapshot &&
        (!before || c.id < before) &&
        c.name.toLowerCase().includes(q.toLowerCase()) &&
        (!type || c.type === type) &&
        (!status || c.status === status),
    )
    .sort((a, b) => b.id - a.id);
  const items = filtered.slice(0, limit),
    has_more = filtered.length > limit;
  return {
    items,
    has_more,
    snapshot_cursor,
    read_only: false,
    next_cursor: has_more ? encodeURIComponent(JSON.stringify({ binding, before: items.at(-1)?.id, snapshot })) : "",
  };
}
