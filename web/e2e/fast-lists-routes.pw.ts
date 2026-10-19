import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// The drive list leaves each drive's route out; the routes come only where a map is drawn (many drives: the map views) or
// with one drive (its own page).

async function seen(page: Page) {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  const calls: { path: string; slim: boolean }[] = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (r.method() === "GET" && (u.pathname === "/api/drives" || /^\/api\/drives\/[^/]+$/.test(u.pathname))) calls.push({ path: u.pathname, slim: u.searchParams.get("slim") === "1" });
  });
  return calls;
}
const lists = (c: { path: string; slim: boolean }[]) => c.filter((x) => x.path === "/api/drives");

test("Now and the Drives table ask for the slim list; only the map view asks for the routes", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const calls = await seen(page);
  await page.goto("/");
  await expect.poll(() => lists(calls).length).toBeGreaterThan(0);
  expect(lists(calls).every((c) => c.slim)).toBe(true);
  await page.goto("/drives");
  await page.waitForTimeout(800);
  expect(lists(calls).every((c) => c.slim)).toBe(true);
  // The map view draws many drives' routes: now (and only now) the full list is fetched.
  await page.getByRole("button", { name: /^Map$/ }).click();
  await expect.poll(() => lists(calls).filter((c) => !c.slim).length).toBe(1);
  await ctx.close();
});

test("a drive's page asks for that one drive, which carries its route", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const calls = await seen(page);
  await page.goto("/drives");
  await page.getByRole("link").filter({ hasText: /mi/ }).first().click();
  await expect.poll(() => calls.filter((c) => /^\/api\/drives\/[^/]+$/.test(c.path)).length).toBeGreaterThan(0);
  await ctx.close();
});
