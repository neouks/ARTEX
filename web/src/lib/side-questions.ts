import { http } from "@/lib/api";

export interface SideModel {
  model: string;
  name: string;
  format: string;
  profile_id: number;
}
export interface SideExchange {
  id: string;
  ordinal: number;
  client_request_id: string;
  question: string;
  answer: string;
  status: "running" | "completed" | "failed" | "cancelled" | "interrupted";
  error?: string;
  model: SideModel;
  snapshot_at: string;
  created_at: string;
  sequence: number;
  context?: {
    phase?: "preparing" | "summarizing_history" | "compressing_snapshot" | "retrying" | "answering";
    recent_exchanges: number;
    history_summarized: boolean;
    snapshot_summarized: boolean;
    estimated_input_tokens?: number;
    input_budget?: number;
    output_tokens?: number;
    overflow_retried?: boolean;
  };
}
export interface SideHistory {
  items: SideExchange[];
  current: SideExchange | null;
  next_cursor: number;
  snapshot: { captured_at: string; model: SideModel; available: boolean; reason: string } | null;
}

// Validate before scheduling React state updates: updater errors escape the
// request's try/catch and otherwise crash the entire session view.
export function parseSideHistory(value: unknown): SideHistory {
  const data = value as SideHistory | null;
  if (
    !data ||
    !(Array.isArray(data.items) || data.items === null) ||
    !Number.isInteger(data.next_cursor) ||
    data.next_cursor < 0
  ) {
    throw new Error("旁路问答历史响应格式错误，请重试");
  }
  const items = data.items ?? [];
  for (const item of items) parseSideExchange(item);
  if (data.current != null) parseSideExchange(data.current);
  return { ...data, items, current: data.current ?? null, snapshot: data.snapshot ?? null };
}

export function parseSideExchange(value: unknown): SideExchange {
  const item = value as SideExchange | null;
  if (
    !item ||
    typeof item.id !== "string" ||
    !item.id ||
    !Number.isInteger(item.sequence) ||
    !Number.isInteger(item.ordinal) ||
    typeof item.question !== "string" ||
    typeof item.answer !== "string" ||
    !["running", "completed", "failed", "cancelled", "interrupted"].includes(item.status) ||
    !item.model ||
    typeof item.model.model !== "string"
  ) {
    throw new Error("旁路问答记录格式错误，请重试");
  }
  return item;
}

async function request<T>(path: string, method = "GET", body?: unknown): Promise<T> {
  return http<T>(path.replace(/^\/api/, ""), { method, body: body === undefined ? undefined : JSON.stringify(body) });
}

export const sideAPI = {
  history: async (parent: string, before = 0) =>
    parseSideHistory(await request<unknown>(`${parent}/side-questions?before=${before}`)),
  ask: (parent: string, question: string, client_request_id: string) =>
    request<unknown>(`${parent}/side-questions`, "POST", { question, client_request_id }).then(parseSideExchange),
  clear: (parent: string) => request(`${parent}/side-questions`, "DELETE"),
  cancel: (id: string) => request(`/api/side-questions/${id}/cancel`, "POST"),
};

export function isBtwCommand(text: string) {
  return /^\/btw(?:\s|$)/.test(text.trim());
}
