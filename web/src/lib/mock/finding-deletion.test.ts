import { mockFindingDeletionFeedback, mockHandle } from "./handler";
import assert from "node:assert/strict";
import test from "node:test";

test("删除漏洞保留原因，非法原因不删除，重复删除不重复反馈", async () => {
  const rows = await mockHandle<{ id: string; task_id?: string }[]>("GET", "/exploration/findings");
  const finding = rows[0];
  assert.ok(finding);
  const path = `/exploration/findings/${finding.id}`;
  await assert.rejects(mockHandle("DELETE", path, JSON.stringify({ reason: "中".repeat(2001) })));
  assert.ok(await mockHandle("GET", path));
  const count = mockFindingDeletionFeedback.length;
  const result = await mockHandle<{ deleted: boolean }>("DELETE", path, JSON.stringify({ reason: "  证据不足  " }));
  assert.equal(result.deleted, true);
  assert.equal(mockFindingDeletionFeedback.length, count + 1);
  assert.equal(mockFindingDeletionFeedback.at(-1)?.reason, "证据不足");
  await assert.rejects(mockHandle("DELETE", path));
  assert.equal(mockFindingDeletionFeedback.length, count + 1);
});
