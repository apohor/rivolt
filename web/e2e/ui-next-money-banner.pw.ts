import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Money's "sample data" banner (ledger fixture mode) sits above the next-mode
// desktop grid, full width, not below it.
test("sample-data banner is above the Money grid, full width, in next-mode desktop", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.theme", "dark");
  });
  await stubApi(page);
  // No ledger service: the page falls back to the built-in example statement.
  await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ status: 404, body: "not found" }));
  await page.route("**/api/ledger/summary**", (r) => r.fulfill({ status: 404, body: "not found" }));
  await page.goto("/money");
  const banner = page.getByRole("note").filter({ hasText: "Sample data" });
  await expect(banner).toBeVisible();
  const hero = page.getByRole("region", { name: "Statement" });
  await expect(hero).toBeVisible();
  const b = (await banner.boundingBox())!;
  const h = (await hero.boundingBox())!;
  const main = (await page.locator("main").boundingBox())!;
  expect(b.y + b.height).toBeLessThanOrEqual(h.y + 1); // above the statement
  expect(b.width).toBeGreaterThan(main.width * 0.95); // full width
  await page.screenshot({ path: "e2e/__screenshots__/uinext-money-banner-desktop-next.png" });
  await ctx.close();
});

test("next desktop: Money's receipts card matches the left column's top and bottom edges and scrolls inside", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.theme", "dark");
  });
  await stubApi(page);
  await page.goto("/money");
  const hero = page.locator("[data-ui-area='m-hero']");
  const split = page.locator("[data-ui-area='m-split']");
  const rec = page.locator("[data-ui-area='m-receipts']");
  await expect(rec).toBeVisible();
  const a = (await hero.boundingBox())!;
  const s = (await split.boundingBox())!;
  const r = (await rec.boundingBox())!;
  expect(Math.abs(r.y - a.y)).toBeLessThanOrEqual(2);
  expect(Math.abs(r.y + r.height - (s.y + s.height))).toBeLessThanOrEqual(2);
  // The list scrolls inside the card.
  const scrolls = await rec.locator("> div:last-child").evaluate((el) => el.scrollHeight > el.clientHeight);
  expect(scrolls).toBe(true);
  await page.screenshot({ path: "e2e/__screenshots__/uinext-money-parity-desktop.png" });
  await ctx.close();
});
