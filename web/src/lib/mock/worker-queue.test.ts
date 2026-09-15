import type { TaskNode } from "../types";
import { mockHandle } from "./handler";
import assert from "node:assert/strict";
import test from "node:test";

type Queue = { items: TaskNode[]; version: number; manual: boolean };
const base = "/tasks/t-acme-api";
test("队列手动排序、版本冲突、取消后重开追加队尾和物理删除", async () => {
  const queue = () => mockHandle<Queue>("GET", `${base}/worker-queue`);
  const control = (id: string, action: string) =>
    mockHandle("POST", `${base}/intents/${id}/control`, JSON.stringify({ action }));
  const first = await queue();
  assert.ok(first.items.length >= 3);
  const a = first.items[0].id,
    b = first.items[1].id;
  const moved = await mockHandle<Queue>(
    "POST",
    `${base}/worker-queue/move`,
    JSON.stringify({ id: b, before_id: a, version: first.version }),
  );
  assert.equal(moved.items[0].id, b);
  assert.equal(moved.manual, true);
  await assert.rejects(
    mockHandle("POST", `${base}/worker-queue/move`, JSON.stringify({ id: a, before_id: null, version: first.version })),
  );
  await control(b, "cancel");
  assert.ok(!(await queue()).items.some((item) => item.id === b));
  await control(b, "resume");
  assert.equal((await queue()).items.at(-1)?.id, b);
  await mockHandle("DELETE", `${base}/intents/${b}`);
  await mockHandle("DELETE", `${base}/intents/${b}`);
  assert.ok(!(await queue()).items.some((item) => item.id === b));
  await assert.rejects(control(b, "resume"));
  const all = await mockHandle<TaskNode[]>("GET", "/exploration/intents");
  const running = all.find((item) => item.state === "running");
  assert.ok(running);
  await assert.rejects(mockHandle("DELETE", `${base}/intents/${running.id}`));
});
