import assert from "node:assert/strict";
import test from "node:test";
import { traffic, trafficDetail } from "./data";
import { MockTraffic } from "./traffic";

test("traffic mock filters, sorts, paginates, and clears independently of filters", () => {
  const store = new MockTraffic(traffic, trafficDetail);
  const query = (q: string) => store.page(new URLSearchParams(q));
  assert.equal(query("status=4xx&path=/v1/admin").total, 1);
  assert.equal(query("body=北京市").total, traffic.count);
  assert.equal(query("body=nonexistent").total, 0);
  const first = query("sort=resp_len&order=asc&size=1"), next = query("sort=resp_len&order=asc&size=1&page=1");
  assert.notEqual(first.exchanges![0].id, next.exchanges![0].id);
  assert.equal(query("resp_min=10000&resp_max=15000").total, 2);
  assert.equal(store.remove().deleted, traffic.count);
  assert.equal(query("").count, 0);
  assert.equal(store.hosts().hosts.length, 0);
  assert.equal(store.remove().deleted, 0);
  assert.equal(traffic.exchanges!.length, traffic.count);
});
