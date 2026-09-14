import { mockHandle } from "./mock/handler";
import { parseSideExchange, parseSideHistory } from "./side-questions";
import assert from "node:assert/strict";
import test from "node:test";

test("旁路历史拒绝异常响应，兼容 Go 空切片", () => {
  for (const value of [undefined, null, [], {}, { items: {}, next_cursor: 0 }, { items: [null], next_cursor: 0 }]) {
    assert.throws(() => parseSideHistory(value), /格式错误/);
  }
  assert.deepEqual(parseSideHistory({ items: null, next_cursor: 0 }).items, []);
  assert.throws(() => parseSideExchange({ ok: true }), /格式错误/);
});

test("三种旁路父会话的 Mock 都返回合法历史且不伪造问答成功", async () => {
  for (const parent of ["/conversations/1", "/tasks/t-acme-web/chat", "/tasks/t-acme-web/intents/1"]) {
    const data = parseSideHistory(await mockHandle("GET", `${parent}/side-questions?before=0`));
    assert.deepEqual(data.items, []);
    assert.equal(data.next_cursor, 0);
    assert.equal(data.snapshot, null);
    await assert.rejects(mockHandle("POST", `${parent}/side-questions`, JSON.stringify({ question: "测试" })), /Mock/);
    assert.deepEqual(await mockHandle("DELETE", `${parent}/side-questions`), { cleared: true });
  }
});
