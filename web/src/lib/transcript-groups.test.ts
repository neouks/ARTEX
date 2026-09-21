import assert from "node:assert/strict";
import test from "node:test";
import { groupSteps, groupProcesses, processContainsActivity } from "./transcript-groups";
import type { Activity } from "./types";
const step = (seq: number, kind: Activity["kind"], extra: Partial<Activity> = {}): Activity => ({
  seq,
  kind,
  worker: "mainagent",
  ts: "",
  summary: "example",
  ...extra,
});
test("parallel calls pair by ID and form one process without mutating history", () => {
  const rows = [
    step(1, "tool_use", { tool_use_id: "a" }),
    step(2, "tool_use", { tool_use_id: "b" }),
    step(3, "tool_result", { tool_use_id: "b" }),
    step(4, "tool_result", { tool_use_id: "a" }),
  ];
  const before = JSON.stringify(rows);
  const groups = groupProcesses(groupSteps(rows, true));
  assert.equal(groups.length, 1);
  const g = groups[0];
  assert.equal(g.type, "process");
  if (g.type !== "process") return;
  assert.equal(g.groups.length, 2);
  assert.equal(processContainsActivity(g, 4), true);
  assert.equal(processContainsActivity(g, 9), false);
  assert.equal(JSON.stringify(rows), before);
});
test("answers, rounds, approvals and errors remain outside process groups", () => {
  const groups = groupProcesses(
    groupSteps(
      [
        step(1, "thinking"),
        step(2, "text"),
        step(3, "tool_use", { tool_use_id: "a" }),
        step(4, "tool_result", { tool_use_id: "a", is_error: true }),
        step(5, "intercept_request"),
        step(6, "round"),
        step(7, "user"),
      ],
      true,
    ),
  );
  assert.deepEqual(
    groups.map((g) => g.type),
    ["process", "answer", "tool", "intercept", "round", "user"],
  );
});
test("late results preserve process identity; roles never merge", () => {
  const rows = [step(1, "tool_use", { tool_use_id: "a" }), step(2, "thinking")];
  const before = groupProcesses(groupSteps(rows, true));
  const after = groupProcesses(
    groupSteps(
      [...rows, step(3, "tool_result", { tool_use_id: "a" }), step(4, "thinking", { worker: "planner" })],
      true,
    ),
  );
  assert.equal(before[0].key, after[0].key);
  assert.equal(after.length, 2);
});
test("legacy worker text remains compact outside task variant", () => {
  assert.equal(groupSteps([step(1, "text")], false)[0].type, "msg");
  assert.equal(groupSteps([step(1, "text")], true)[0].type, "answer");
});
