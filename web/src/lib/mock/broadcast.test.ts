import assert from "node:assert/strict";
import test from "node:test";
import { mockHandle } from "./handler";
import * as D from "./data";
import type { ExplorationNodePage, ExplorationNodeDetail } from "../types";

test("broadcast mock separates list summaries from paged details and isolates tasks", async () => {
  const task = D.tasks[0].id;
  const page = await mockHandle<ExplorationNodePage>("GET", `/exploration/nodes?task=${task}`);
  assert.ok(page.items.length);
  assert.deepEqual(page.edges, []);
  assert.deepEqual(page.refs, {});
  for (const row of page.items) {
    const payload = JSON.parse(row.payload || "{}");
    assert.deepEqual(Object.keys(payload), ["summary"]);
    assert.ok(Array.from(payload.summary).length <= 500);
  }
  const node = page.items[0];
  const detail = await mockHandle<ExplorationNodeDetail>("GET", `/exploration/nodes/${node.id}?task=${task}`);
  assert.equal(detail.node.id, node.id);
  assert.ok(detail.edges.length <= 50);
  assert.ok(Array.from(detail.payload).length <= 16000);
  const count = await mockHandle<ExplorationNodePage>("GET", `/exploration/nodes?task=${task}&count_only=1`);
  assert.equal(count.total, page.total);
  assert.deepEqual(count.items, []);
  await assert.rejects(mockHandle("GET", `/exploration/nodes/${node.id}?task=unknown`), /not found/);
});
