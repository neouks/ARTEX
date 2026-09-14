import { mockHandle } from "./mock/handler";
import { mockToolCalls } from "./mock/tool-calls";
import { extraToolName, parseToolCallPage, type ToolCallText, toolCallRevision, toolCallURL } from "./tool-calls";
import type { Activity } from "./types";
import assert from "node:assert/strict";
import test from "node:test";

const event = (seq: number, kind: Activity["kind"], tool_use_id = "repeat", extra = {}): Activity => ({
  seq,
  kind,
  worker: "mainagent",
  tool: "Bash",
  tool_use_id,
  ts: "2026-09-14T00:00:00Z",
  summary: "",
  ...extra,
});

test("只响应调用和执行边界，不为普通流式文本重复查询", () => {
  const use = event(1, "tool_use");
  assert.equal(toolCallRevision([use], true), toolCallRevision([use, event(2, "text"), event(3, "thinking")], true));
  assert.notEqual(toolCallRevision([use], true), toolCallRevision([use, event(4, "tool_result")], true));
  assert.notEqual(toolCallRevision([use], true), toolCallRevision([use], false));
});

test("调用位置配对、跨页、状态筛选和稳定游标", () => {
  const events = [
    event(1, "tool_use"),
    event(2, "tool_result"),
    event(3, "tool_use"),
    event(50, "tool_result", "repeat", { is_error: true }),
    event(51, "tool_result", "orphan"),
    event(52, "tool_use", "missing"),
    event(53, "result"),
    event(54, "tool_use", "live"),
  ];
  const first = parseToolCallPage(mockToolCalls(events, [], new URLSearchParams({ limit: "2" }), "s", true));
  assert.deepEqual(
    first.items.map((c) => [c.id, c.status]),
    [
      [54, "running"],
      [52, "missing"],
    ],
  );
  assert.ok(first.has_more);
  const second = parseToolCallPage(
    mockToolCalls(events, [], new URLSearchParams({ limit: "2", cursor: first.next_cursor }), "s", true),
  );
  assert.deepEqual(
    second.items.map((c) => [c.id, c.use_id, c.result_id]),
    [
      [51, null, 51],
      [3, 3, 50],
    ],
  );
  const failed = parseToolCallPage(mockToolCalls(events, [], new URLSearchParams({ status: "failed", q: "ba" }), "s"));
  assert.deepEqual(
    failed.items.map((c) => c.id),
    [3],
  );
  assert.throws(() => mockToolCalls(events, [], new URLSearchParams({ cursor: first.next_cursor }), "other"), /游标/);
  assert.throws(
    () => mockToolCalls(events, [], new URLSearchParams({ cursor: first.next_cursor, q: "changed" }), "s"),
    /游标/,
  );
  events.push(event(55, "tool_result", "live"), event(56, "tool_use", "new"));
  const stable = parseToolCallPage(
    mockToolCalls(events, [], new URLSearchParams({ limit: "2", cursor: first.next_cursor }), "s"),
  );
  assert.deepEqual(
    stable.items.map((c) => c.id),
    [51, 3],
  );
});

test("轮次与空 ID 不按名称误配对，长中文安全分段", () => {
  const events = [
    event(1, "tool_use"),
    event(2, "result"),
    event(3, "tool_result"),
    event(4, "tool_use", ""),
    event(5, "tool_result", ""),
  ];
  const page = parseToolCallPage(mockToolCalls(events, [], new URLSearchParams(), "s"));
  assert.equal(page.items.length, 4);
  assert.equal(page.items.find((c) => c.id === 3)?.use_id, null);
  const body = "<script>不执行😀</script>".repeat(900);
  events.push(event(6, "tool_result", "long", { detail: body }));
  let rebuilt = "",
    offset = 0;
  for (;;) {
    const p = mockToolCalls(
      events,
      [],
      new URLSearchParams({ offset: String(offset), limit: "103" }),
      "s",
      false,
      6,
    ) as ToolCallText;
    rebuilt += p.text;
    if (!p.has_more) break;
    offset = p.next_offset;
  }
  assert.equal(rebuilt, body);
  assert.throws(() => mockToolCalls(events, [], new URLSearchParams(), "s", false, 99), /session/);
});

test("查询地址保留任务会话，异常响应明确报错，封装工具不伪造调用", () => {
  const u = new URL(toolCallURL("/exploration/tool-calls?task=1&session=main%3A2", {}, 3, 8000), "http://localhost");
  assert.equal(u.pathname, "/exploration/tool-calls/3");
  assert.equal(u.searchParams.get("session"), "main:2");
  assert.equal(u.searchParams.get("offset"), "8000");
  for (const value of [null, [], {}, { items: {} }]) assert.throws(() => parseToolCallPage(value), /格式错误/);
  assert.equal(extraToolName("ExecuteExtraTool", '{"tool_name":"my_custom","params":{}}'), "my_custom");
  assert.equal(extraToolName("ExecuteExtraTool", '{"tool_name":'), null);
  assert.equal(extraToolName("Bash", '{"tool_name":"pretend"}'), null);
});

test("Mock 路由覆盖两类会话，不把旧任务记录给空任务或新会话分段", async () => {
  const page = parseToolCallPage(await mockHandle("GET", "/exploration/tool-calls?task=t-acme-web&session=plan"));
  assert.ok(page.items.length > 0);
  const empty = parseToolCallPage(await mockHandle("GET", "/exploration/tool-calls?task=t-acme-api&session=plan"));
  assert.equal(empty.items.length, 0);
  const segment = parseToolCallPage(await mockHandle("GET", "/exploration/tool-calls?task=t-acme-web&session=main:99"));
  assert.equal(segment.items.length, 0);
  const chat = parseToolCallPage(await mockHandle("GET", "/conversations/1/tool-calls"));
  assert.ok(Array.isArray(chat.items));
  await assert.rejects(mockHandle("GET", "/conversations/999999/tool-calls"), /not found/);
});
