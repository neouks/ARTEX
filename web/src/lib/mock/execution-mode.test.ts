import assert from "node:assert/strict";
import test from "node:test";
import { mockHandle } from "./handler";

test("manual selection persists, repeated mode is idempotent, and finish preserves work", async () => {
  const base = "/tasks/t-acme-api";
  type Queue = { execution_mode: string; items: { id: string; payload: string }[] };
  const queue = () => mockHandle<Queue>("GET", `${base}/worker-queue`);
  await mockHandle("PATCH", `${base}/execution-mode`, JSON.stringify({ execution_mode: "manual" }));
  const first = await queue();
  assert.equal(first.execution_mode, "manual");
  assert.ok(first.items.length > 0);
  const id = first.items[0].id;
  const dispatch = await mockHandle<{ results: { status: string }[] }>(
    "POST",
    `${base}/intents/dispatch`,
    JSON.stringify({ intent_ids: [id, id, "missing"] }),
  );
  assert.deepEqual(
    dispatch.results.map((n) => n.status),
    ["dispatched", "rejected"],
  );
  await mockHandle("PATCH", `${base}/execution-mode`, JSON.stringify({ execution_mode: "manual" }));
  assert.equal(JSON.parse((await queue()).items.find((n) => n.id === id)!.payload).dispatch_requested, true);
  await mockHandle("PATCH", `${base}/execution-mode`, JSON.stringify({ execution_mode: "managed" }));
  await mockHandle("PATCH", `${base}/execution-mode`, JSON.stringify({ execution_mode: "manual" }));
  assert.equal(JSON.parse((await queue()).items.find((n) => n.id === id)!.payload).dispatch_requested, undefined);
  await mockHandle("POST", `${base}/control`, JSON.stringify({ action: "finish" }));
  assert.ok((await queue()).items.some((n) => n.id === id));
  await assert.rejects(mockHandle("PATCH", `${base}/execution-mode`, JSON.stringify({ execution_mode: "bad" })));
});
