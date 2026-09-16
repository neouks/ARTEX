import assert from "node:assert/strict";
import test from "node:test";
import type { Activity, InterceptExecution } from "../types";
import { mockHandle } from "./handler";

test("动作审批定位原始会话，资产来源仍可独立定位", async () => {
  const source = await mockHandle<InterceptExecution>("GET", "/intercept/history/94/execution");
  assert.equal(source.session, "intent:i-1");
  assert.equal(source.task_id, "t-acme-web");
  assert.deepEqual(
    source.items.map((a) => a.kind),
    ["tool_use", "tool_result"],
  );
  const page = await mockHandle<{ items: Activity[] }>(
    "GET",
    `/exploration/activity/history?task=${source.task_id}&session=${source.session}`,
  );
  assert.ok(page.items.some((a) => a.seq === source.seq && a.tool_use_id === "call-write-report"));
  await assert.rejects(mockHandle("GET", "/intercept/history/99999/execution"));
  await assert.rejects(mockHandle("GET", "/intercept/history/94/execution?conversation=1"));
});
