import assert from "node:assert/strict";
import test from "node:test";
import type { TaskNode } from "../types";
import { mockHandle } from "./handler";

test("等待 Worker 取消保留记录、重复取消幂等、手动重新开启解除标记", async () => {
  const rows = await mockHandle<TaskNode[]>("GET", "/exploration/intents");
  const waiting = rows.find((row) => row.state === "open" && !row.inherited);
  assert.ok(waiting);
  const path = `/tasks/t-acme-api/intents/${waiting.id}/control`;
  const cancel = await mockHandle<{ state: string; cancelled_by_user: boolean; cancel_reason: string }>(
    "POST",
    path,
    JSON.stringify({ action: "cancel" }),
  );
  assert.equal(cancel.state, "stopped");
  assert.equal(cancel.cancelled_by_user, true);
  assert.equal(cancel.cancel_reason, "用户取消等待运行");
  const retained = (await mockHandle<TaskNode[]>("GET", "/exploration/intents")).find((row) => row.id === waiting.id);
  assert.ok(retained);
  assert.equal(JSON.parse(retained.payload || "{}").cancelled_by_user, true);
  const again = await mockHandle<typeof cancel>(
    "POST",
    path,
    JSON.stringify({ action: "cancel", reason: "duplicate" }),
  );
  assert.deepEqual(again, cancel);
  const resumed = await mockHandle<{ state: string; cancelled_by_user: boolean }>(
    "POST",
    path,
    JSON.stringify({ action: "resume" }),
  );
  assert.equal(resumed.state, "open");
  assert.equal(resumed.cancelled_by_user, false);
  assert.equal(JSON.parse(retained.payload || "{}").cancelled_by_user, false);
});

test("取消运行中 Worker 保留原因，用户重跑解除取消状态", async () => {
  const rows = await mockHandle<TaskNode[]>("GET", "/exploration/intents");
  const running = rows.find((row) => row.state === "running" && !row.inherited);
  assert.ok(running);
  const base = `/tasks/t-acme-api/intents/${running.id}`;
  await mockHandle("POST", `${base}/control`, JSON.stringify({ action: "cancel", reason: "用户指定原因" }));
  assert.equal(JSON.parse(running.payload || "{}").cancel_reason, "用户指定原因");
  await mockHandle("POST", `${base}/rerun`);
  assert.equal(running.state, "open");
  assert.equal(JSON.parse(running.payload || "{}").cancelled_by_user, false);
  running.inherited = true;
  try {
    await assert.rejects(mockHandle("POST", `${base}/control`, JSON.stringify({ action: "cancel" })));
  } finally {
    running.inherited = false;
  }
});
