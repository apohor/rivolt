import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// /lease-calculator: public and signed out, computed entirely in the page.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function fill(page: Page, v: { start: string; end: string; allowed: string; startOdo: string; odo: string; rate: string }) {
  await page.getByLabel("Lease start date").fill(v.start);
  await page.getByLabel("Lease end date").fill(v.end);
  await page.getByLabel(/allowed for the whole lease/).fill(v.allowed);
  await page.getByLabel(/Odometer at lease start/).fill(v.startOdo);
  await page.getByLabel(/Odometer today/).fill(v.odo);
  await page.getByLabel(/^Overage rate per/).fill(v.rate);
  await page.getByRole("button", { name: "Calculate" }).click();
}

const VECTOR = { start: "2025-01-01", end: "2028-01-01", allowed: "36000", startOdo: "10", odo: "10010", rate: "0.25" };

async function open(page: Page) {
  await page.clock.setFixedTime(new Date("2026-01-01T12:00:00"));
  await stubApi(page, { signedIn: false });
  await page.goto("/lease-calculator");
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`lease calculator · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page);
      await expect(page.getByRole("heading", { name: "Rivian lease mileage calculator", level: 1 })).toBeVisible();
      await fill(page, VECTOR);
      const res = page.locator("[data-lease-result]");
      await expect(res).toBeVisible();
      await expect(res.getByRole("heading", { name: "Where you stand" })).toBeVisible();
      await expect(res.locator("[data-lc-used]")).toHaveText("10,000");
      await expect(res.locator("[data-lc-allowed]")).toHaveText("12,000");
      await expect(res.locator("[data-lc-standing]")).toHaveText("You're 2,000 mi under plan");
      await expect(res.locator("[data-lc-projected]")).toHaveText("30,000 mi");
      await expect(res.locator("[data-lc-overage]")).toHaveText("Projected to finish within your allowance");
      await expect(res.locator("[data-lc-budget]")).toHaveText("About 36 mi a day");
      await expect(page.getByText("Rivolt is unofficial and not affiliated with, endorsed by or sponsored by Rivian.")).toBeVisible();
      await expect(page.getByRole("link", { name: /Create an account|Sign in/ })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `lease-calculator-${w}-${theme}.png`), fullPage: true });
      await ctx.close();
    });
  }
}

test("an overage is projected and priced; kilometers convert both ways", async ({ page }) => {
  await open(page);
  await fill(page, { ...VECTOR, odo: "20010" });
  await expect(page.locator("[data-lc-projected]")).toHaveText("60,000 mi");
  await expect(page.locator("[data-lc-overage]")).toHaveText("24,000 mi, about $6,000.00");
  await expect(page.locator("[data-lc-standing]")).toHaveText("You're 8,000 mi over plan");
  // The same lease in kilometers: 36000 mi = 57936 km allowed, odometers in km, rate per km.
  await page.getByRole("button", { name: "Kilometers" }).click();
  await fill(page, { ...VECTOR, allowed: "57936", startOdo: "16.1", odo: "32200", rate: "0.1553" });
  await expect(page.locator("[data-lc-projected]")).toHaveText("96,552 km"); // about 60,000 mi
  await expect(page.locator("[data-lc-budget]")).toContainText("km a day");
  await expect(page.getByText("Kilometers driven so far")).toBeVisible();
});

test("the first two weeks show the standing but no projection", async ({ page }) => {
  await open(page);
  await fill(page, { ...VECTOR, start: "2025-12-25", end: "2028-12-25", odo: "300" });
  await expect(page.locator("[data-lc-used]")).toBeVisible();
  await expect(page.locator("[data-lc-too-early]")).toContainText("Too early to project a pace. Check back after a couple of weeks of driving.");
  await expect(page.locator("[data-lc-projected]")).toHaveCount(0);
});

test("bad input shows a message per field and no result", async ({ page }) => {
  await open(page);
  await fill(page, { start: "2025-01-01", end: "2024-01-01", allowed: "", startOdo: "500", odo: "100", rate: "" });
  await expect(page.locator("[data-lease-result]")).toHaveCount(0);
  await expect(page.getByText("The end date must be after the start")).toBeVisible();
  await expect(page.getByText("Enter the total miles the lease allows")).toBeVisible();
  await expect(page.getByText("The odometer now can't be below the starting odometer")).toBeVisible();
  await expect(page.getByText("Enter the overage rate (0 if none)")).toBeVisible();
});

test("nothing leaves the browser and nothing is stored", async ({ page }) => {
  const calls: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname.startsWith("/api/")) calls.push(`${r.method()} ${new URL(r.url()).pathname}`);
  });
  await open(page);
  await page.waitForLoadState("networkidle");
  await expect(page.getByRole("heading", { name: "Rivian lease mileage calculator" })).toBeVisible();
  const before = calls.length;
  await fill(page, VECTOR);
  await expect(page.locator("[data-lease-result]")).toBeVisible();
  expect(calls.slice(before)).toEqual([]); // computing makes no request
  expect(calls.filter((c) => !c.startsWith("GET "))).toEqual([]); // the page's own calls are reads only
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
  for (const v of ["36000", "10010", "2028-01-01", "0.25"]) expect(stored).not.toContain(v);
});
