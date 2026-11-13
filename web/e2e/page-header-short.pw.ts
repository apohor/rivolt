import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// At Safari's aA 300% a phone is 131 x 232 CSS px. The page header (title and period, 80 to 100 px) pinned under the app bar, with the tab bar 57 px
// below, left a strip for the content (#1965). On a viewport that short the page header scrolls with the page; at 390 x 844 it stays pinned.
for (const path of ["/", "/money", "/activity"]) {
  test(`@webkit page header scrolls away on a 232 px viewport · ${path}`, async ({ browser }, info) => {
    const ctx = await browser.newContext({ viewport: { width: 131, height: 232 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.goto(path);
    await expect(page.locator("[data-ui-header]")).toBeVisible();
    const pos = await page.evaluate(() => [
      getComputedStyle(document.querySelector("header")!).position,
      getComputedStyle(document.querySelector("[data-ui-header]")!).position,
    ]);
    expect(pos, "app bar pinned, page header not").toEqual(["sticky", "static"]);
    await page.evaluate(() => window.scrollTo(0, 400));
    await page.waitForTimeout(200);
    const top = await page.evaluate(() => document.querySelector("[data-ui-header]")!.getBoundingClientRect().bottom);
    expect(top, "the header scrolled out of sight").toBeLessThanOrEqual(0);
    const shot = info.outputPath("header-aa300.png");
    await page.screenshot({ path: shot });
    await info.attach("header-aa300", { path: shot, contentType: "image/png" });
  });
}

test("@webkit page header stays pinned on a 390 px iPhone", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.goto("/money");
  await expect(page.locator("[data-ui-header]")).toBeVisible();
  const pos = await page.evaluate(() => [
    getComputedStyle(document.querySelector("header")!).position,
    getComputedStyle(document.querySelector("[data-ui-header]")!).position,
  ]);
  expect(pos).toEqual(["sticky", "sticky"]);
});
