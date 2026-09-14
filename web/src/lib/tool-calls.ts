import type { Activity } from "./types";

// Text/thinking/usage deltas do not change the tool-call view. Reuse the existing
// stream without turning token streaming into repeated database queries.
export function toolCallRevision(events: Activity[], running: boolean | undefined): string {
  for (let i = events.length - 1; i >= 0; i--) {
    if (["tool_use", "tool_result", "result", "round", "user"].includes(events[i].kind))
      return `${events[i].seq}:${!!running}`;
  }
  return `0:${!!running}`;
}

export type ToolCallType = "builtin" | "custom" | "mcp" | "unknown";
export type ToolCallStatus = "success" | "failed" | "running" | "missing";
export interface ToolCall {
  id: number;
  name: string;
  type: ToolCallType;
  status: ToolCallStatus;
  created_at: string;
  use_id: number | null;
  result_id: number | null;
  tool_use_id: string;
}
export interface ToolCallPage {
  items: ToolCall[];
  has_more: boolean;
  next_cursor: string;
  snapshot_cursor: number;
  source_task_id?: number;
  read_only: boolean;
}
export interface ToolCallText {
  text: string;
  offset: number;
  next_offset: number;
  total: number;
  has_more: boolean;
}
export interface ToolCallFilter {
  q?: string;
  type?: string;
  status?: string;
  cursor?: string;
}

export function parseToolCallPage(value: unknown): ToolCallPage {
  const p = value as ToolCallPage | null;
  if (
    !p ||
    !Array.isArray(p.items) ||
    typeof p.has_more !== "boolean" ||
    typeof p.next_cursor !== "string" ||
    !Number.isSafeInteger(p.snapshot_cursor) ||
    p.items.some(
      (c) =>
        !c ||
        !Number.isSafeInteger(c.id) ||
        typeof c.name !== "string" ||
        !["builtin", "custom", "mcp", "unknown"].includes(c.type) ||
        !["success", "failed", "running", "missing"].includes(c.status),
    )
  )
    throw new Error("工具调用响应格式错误");
  return p;
}

// base already carries the selected session. Detail IDs never replace that scope.
export function toolCallURL(base: string, filters: ToolCallFilter = {}, id?: number, offset = 0) {
  const [path, query] = base.split("?");
  const params = new URLSearchParams(query);
  for (const [key, value] of Object.entries(filters)) if (value) params.set(key, value);
  if (id != null) params.set("offset", String(offset));
  return `${path}${id == null ? "" : `/${id}`}?${params}`;
}

export function extraToolName(name: string, input: string): string | null {
  if (name !== "ExecuteExtraTool") return null;
  try {
    const args = JSON.parse(input);
    return typeof args?.tool_name === "string" ? args.tool_name : null;
  } catch {
    return null; // a chunk or legacy non-JSON input is not evidence of a child call
  }
}
