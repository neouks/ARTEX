// Real-backend browser regression. Use only an isolated fixture task containing
// an old insert_assets command/result; no mock responses except injected 503s.
// ARTEX_TEST_FIXTURE: JSON {task, anchor, result, approval?}; ARTEX_TEST_TOKEN:
// test-server auth token; ARTEX_TEST_WEB defaults to http://127.0.0.1:3125.
// PLAYWRIGHT_MODULE optionally points to an installed playwright package.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");

async function main() {
  const fixture = JSON.parse(fs.readFileSync(process.env.ARTEX_TEST_FIXTURE, "utf8"));
  const token = process.env.ARTEX_TEST_TOKEN;
  assert.ok(token, "ARTEX_TEST_TOKEN is required");
  const base = process.env.ARTEX_TEST_WEB || "http://127.0.0.1:3125";
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
    await page.context().addCookies([{ name: "artex_token", value: token, url: base }]);
    await page.addInitScript((value) => localStorage.setItem("artex_token", value), token);
    const paths = [
      `/function/tasks/detail?id=${fixture.task}&tab=sessions&session=main:0&activity=${fixture.anchor}`,
    ];
    if (fixture.approval) paths.push(`/function/tasks/detail?id=${fixture.task}&approval=${fixture.approval}`);
    for (const path of paths) {
      let fail = true;
      const endpoint = `**/api/exploration/activity/${fixture.result}?*`;
      await page.route(endpoint, (route) =>
        fail
          ? route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"fixture failure"}' })
          : route.continue(),
      );
      await page.goto(base + path, { waitUntil: "domcontentloaded" });
      const retry = page.getByRole("button", { name: "加载失败，重试" });
      await retry.waitFor({ state: "attached" });
      // Do not scrollIntoView: that would hide the regression. Failed details
      // must be centered by the app itself so a user can discover the retry.
      await page.waitForFunction(() => {
        const el = document.querySelector("[data-source-call]");
        const vp = el?.closest('[data-slot="scroll-area-viewport"]');
        if (!el || !vp) return false;
        const a = el.getBoundingClientRect(), b = vp.getBoundingClientRect();
        return Math.abs(a.top + a.height / 2 - b.top - b.height / 2) < 3;
      });
      assert.equal(await page.locator("[data-source-call]").count(), 1);
      fail = false;
      await retry.click();
      await page.locator("[data-source-call] pre").filter({ hasText: "【输出 ✓】" }).waitFor();
      assert.equal(await retry.count(), 0);
      await page.unroute(endpoint);
      console.log(`PASS: failed source centered and retried: ${path}`);
    }
  } finally {
    await browser.close();
  }
}
main().catch((error) => { console.error(error); process.exitCode = 1; });
