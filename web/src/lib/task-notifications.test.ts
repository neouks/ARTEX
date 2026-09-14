import { MockNotificationLedger, type MockNotificationRecord } from "./mock/task-notifications";
import {
  newerNotificationCursor,
  notificationKey,
  notificationLabel,
  parseNotificationCursor,
} from "./task-notifications";
import assert from "node:assert/strict";
import test from "node:test";

test("角标上限、损坏存储、分类隔离与单向已读进度", () => {
  assert.equal(notificationLabel(100), "99+");
  assert.equal(notificationLabel(2), "2");
  assert.equal(parseNotificationCursor("broken"), undefined);
  assert.equal(parseNotificationCursor('{"snapshot":"x","observed_at":-1}'), undefined);
  assert.notEqual(notificationKey("1", "assets"), notificationKey("1", "findings"));
  assert.notEqual(notificationKey("1", "assets"), notificationKey("2", "assets"));
  const newer = { snapshot: "20", observed_at: 20 };
  assert.deepEqual(newerNotificationCursor(newer, { snapshot: "10", observed_at: 10 }), newer);
  assert.deepEqual(parseNotificationCursor(JSON.stringify(newer)), newer);
});

test("首次无历史提醒；新增、查看、处理、删除与任务范围", () => {
  const ledger = new MockNotificationLedger();
  const records: MockNotificationRecord[] = [{ task: "1", category: "findings", id: "old", pending: true }];
  const query = { task_id: "1", findings: "", assets: "", intercepts: "" };
  const baseline = ledger.summarize([query], "all", records);
  assert.equal(baseline.items[0].findings, 0);
  query.findings = query.assets = query.intercepts = baseline.snapshot;
  records.push(
    { task: "1", category: "findings", id: "new", pending: true },
    { task: "1", category: "assets", id: "host", pending: true },
    { task: "1", category: "intercepts", id: "request", pending: true },
    { task: "2", category: "findings", id: "other", pending: true },
  );
  const next = ledger.summarize([query], "all", records);
  assert.deepEqual(next.items[0], { task_id: "1", findings: 1, assets: 1, intercepts: 1 });
  assert.deepEqual(ledger.summarize([query], "findings", records).items[0], {
    task_id: "1",
    findings: 1,
    assets: 0,
    intercepts: 0,
  });
  query.findings = next.snapshot;
  records[2].pending = false;
  records.splice(3, 1);
  assert.deepEqual(ledger.summarize([query], "all", records).items[0], {
    task_id: "1",
    findings: 0,
    assets: 0,
    intercepts: 0,
  });
});

test("加载快照后新增不被旧快照清除", () => {
  const ledger = new MockNotificationLedger();
  const query = { task_id: "1", findings: "", assets: "", intercepts: "" };
  const before = ledger.summarize([query], "all", []);
  query.findings = before.snapshot;
  const after = ledger.summarize([query], "all", [{ task: "1", category: "findings", id: "late", pending: true }]);
  assert.equal(after.items[0].findings, 1);
});
