import assert from "node:assert/strict";
import test from "node:test";
import { mockHandle } from "./handler";

type SearchPage = { items: { id: number; kind: string; snippet: string }[]; next_cursor: string };
const search = (q: string, extra = "") =>
  mockHandle<SearchPage>(
    "GET",
    `/exploration/activity/search?task=t-acme-web&session=main:0&q=${encodeURIComponent(q)}${extra}`,
  );
test("full history search uses detail, supports literal input and session-bound cursors", async () => {
  const page = await search("new-api.acme.com", "&limit=1");
  assert.equal(page.items.length, 1);
  assert.match(page.items[0].snippet, /new-api.acme.com/);
  const full = await search("NEW-API.ACME.COM");
  assert.equal(full.items[0].id, page.items[0].id);
  const missing = await search("%_'不存在[]");
  assert.deepEqual(missing.items, []);
  await assert.rejects(search(" "));
  await assert.rejects(search("中".repeat(201)));
  await assert.rejects(search("x", "&limit=51"));
  if (page.next_cursor) {
    const next = await search("new-api.acme.com", `&cursor=${encodeURIComponent(page.next_cursor)}`);
    assert.ok(next.items.every((x) => x.id > page.items[0].id));
    await assert.rejects(
      mockHandle(
        "GET",
        `/exploration/activity/search?task=t-acme-api&session=main:0&q=new-api.acme.com&cursor=${encodeURIComponent(page.next_cursor)}`,
      ),
    );
  }
  const window = await mockHandle<{ items: { seq: number }[] }>(
    "GET",
    `/exploration/activity/history?task=t-acme-web&session=main:0&around=${page.items[0].id}`,
  );
  assert.ok(window.items.some((x) => x.seq === page.items[0].id));
});
