import { findingRowKey, selectFinding } from "./findings";
import { mockFindingExport } from "./mock/finding-export";
import { MockFindingTraffic } from "./mock/finding-traffic";
import type { Finding, FindingTraffic } from "./types";
import assert from "node:assert/strict";
import { test } from "node:test";

const item = (id: string, task: string, persistent?: string): Finding => ({
  id,
  task_id: task,
  finding_id: persistent,
  summary: "",
  evidence: "",
  severity: "low",
  status: "pending",
  vulnclass: "test",
  ts: "2026-01-01",
});
test("selection survives refreshed objects, changes page and clears empty results", () => {
  const a = item("7", "1", "100"),
    b = item("8", "1", "101");
  assert.equal(selectFinding([a, b], null), a);
  assert.equal(selectFinding([{ ...a }, { ...b }], findingRowKey(b))?.finding_id, "101");
  assert.equal(selectFinding([a], findingRowKey(b)), a);
  assert.equal(selectFinding([], findingRowKey(a)), null);
  assert.notEqual(findingRowKey(item("100", "1")), findingRowKey(a));
  assert.notEqual(findingRowKey(item("7", "1")), findingRowKey(item("7", "2")));
});
test("demo exports preserve selected report text and offer real ZIP bytes", () => {
  const finding = { ...item("7", "1", "7"), name: "中文漏洞", report: "详细报告", evidence: "证据" };
  const json = mockFindingExport([finding], "json");
  assert.equal(JSON.parse(new TextDecoder().decode(json.bytes))[0].report, "详细报告");
  assert.match(new TextDecoder().decode(mockFindingExport([finding], "md-single").bytes), /证据/);
  assert.match(new TextDecoder().decode(mockFindingExport([finding], "csv").bytes), /中文漏洞/);
  const bytes = mockFindingExport([finding], "md-zip").bytes;
  const view = new DataView(bytes.buffer);
  assert.equal(view.getUint32(0, true), 0x04034b50);
  assert.equal(view.getUint32(bytes.length - 22, true), 0x06054b50);
  const nameSize = view.getUint16(26, true),
    size = view.getUint32(18, true);
  assert.match(new TextDecoder().decode(bytes.slice(30 + nameSize, 30 + nameSize + size)), /详细报告/);
});
test("traffic mock supports versioned atomic edits, order, unlink and read only", () => {
  const store = new MockFindingTraffic();
  const call = (action: string | undefined, method: string, body: Record<string, unknown> = {}, readonly = false) =>
    store.handle("100", action, undefined, method, body, new URLSearchParams(), readonly) as FindingTraffic;
  assert.equal(call(undefined, "GET").bindings.length, 0);
  assert.throws(() => call(undefined, "POST", { traffic_refs: [{ traffic_id: "x-1" }] }, true), /只读/);
  assert.throws(
    () => call(undefined, "POST", { traffic_refs: [{ traffic_id: "x-1" }, { traffic_id: "missing" }] }),
    /不存在/,
  );
  assert.equal(call(undefined, "GET").bindings.length, 0);
  const added = call(undefined, "POST", {
    traffic_refs: [{ traffic_id: "x-1", role: "proof" }, { traffic_id: "x-2" }],
  });
  assert.equal(added.bindings.length, 2);
  const [a, b] = added.bindings.map((item) => item.id);
  assert.throws(() => call(a, "PATCH", { version: 0, role: "proof" }), /版本/);
  const changed = call(a, "PATCH", { version: added.version, role: "baseline", note: "中文说明" });
  assert.equal(changed.bindings[0].note, "中文说明");
  const moved = call("order", "PUT", { version: changed.version, binding_ids: [b, a] });
  assert.equal(moved.bindings[0].id, b);
  const detail = store.handle("100", a, undefined, "GET", {}, new URLSearchParams(), true) as {
    binding: { id: string };
    response: { content: string };
  };
  assert.equal(detail.binding.id, a);
  assert.ok(detail.response.content.includes("order_id"));
  assert.equal(call(a, "DELETE", { version: moved.version }).bindings.length, 1);
  assert.throws(() => call(a, "GET"), /不存在/);
});
