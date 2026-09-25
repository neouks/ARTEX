import assert from "node:assert/strict";
import test from "node:test";
import { lastMainActivity } from "./worker-feedback.ts";

const event = (seq, kind) => ({ seq, kind, worker: "mainagent", summary: "test", ts: "2026-09-24T10:00:00Z" });
const feedback = { ...event(3,"text"), is_error: true, metadata: { worker_feedback: { id: 1, intent_id: 2, state: "blocked" } } };
test("worker feedback neither starts nor settles a main turn", () => {
  const running = event(2,"tool_use"), done = event(4,"result");
  assert.equal(lastMainActivity([event(1,"user"),running,feedback]),running);
  assert.equal(lastMainActivity([done,feedback]),done);
  assert.equal(lastMainActivity([feedback]),undefined);
});
