import assert from "node:assert/strict";
import test from "node:test";
import { startPolling } from "./polling";
const delay = (ms: number) => new Promise((r) => setTimeout(r, ms));
class Visibility extends EventTarget {
  hidden = false;
}
test("slow polls finish, never overlap, and stopping cancels in-flight work", async () => {
  const visibility = new Visibility();
  let active = 0,
    peak = 0,
    completed = 0,
    last: AbortSignal | undefined;
  const poll = startPolling(
    async (signal) => {
      last = signal;
      active++;
      peak = Math.max(peak, active);
      await delay(30);
      if (!signal.aborted) completed++;
      active--;
    },
    5,
    visibility,
    1000,
  );
  try {
    await delay(95);
    assert.ok(completed >= 2);
    assert.equal(peak, 1);
  } finally {
    poll.stop();
  }
  assert.equal(last?.aborted, true);
  const count = completed;
  await delay(40);
  assert.equal(completed, count);
});
test("hidden pages pause and explicit refresh replaces a request without overlap", async () => {
  const visibility = new Visibility();
  let starts = 0,
    active = 0,
    peak = 0;
  const poll = startPolling(
    async (signal) => {
      starts++;
      active++;
      peak = Math.max(peak, active);
      await new Promise<void>((resolve) => signal.addEventListener("abort", () => resolve(), { once: true }));
      active--;
    },
    5,
    visibility,
    1000,
  );
  poll.refresh();
  await delay(5);
  assert.equal(starts, 2);
  assert.equal(peak, 1);
  visibility.hidden = true;
  visibility.dispatchEvent(new Event("visibilitychange"));
  await delay(15);
  assert.equal(starts, 2);
  visibility.hidden = false;
  visibility.dispatchEvent(new Event("visibilitychange"));
  await delay(5);
  assert.equal(starts, 3);
  poll.stop();
});
test("hung request is aborted by its deadline", async () => {
  const visibility = new Visibility();
  let reason: unknown;
  const poll = startPolling(
    async (signal) => {
      await new Promise<void>((resolve) =>
        signal.addEventListener(
          "abort",
          () => {
            reason = signal.reason;
            resolve();
          },
          { once: true },
        ),
      );
    },
    null,
    visibility,
    15,
  );
  await delay(30);
  poll.stop();
  assert.match(String(reason), /超时/);
});
