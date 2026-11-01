import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// Early in the month, with nothing recorded in it yet, Now and Activity follow the last 30 days and say so, instead of
// showing an empty month.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const day = (y: number, m: number, d: number) => new Date(y, m, d, 9, 0, 0).toISOString();
const drive = (id: string, at: string) => ({
  ID: id, VehicleID: VEHICLE_ID, StartedAt: at, EndedAt: new Date(new Date(at).getTime() + 1_800_000).toISOString(), StartSoCPct: 80, EndSoCPct: 70,
  StartOdometerMi: 100, EndOdometerMi: 130, DistanceMi: 30, StartLat: 40, StartLon: -100, EndLat: 40.1, EndLon: -100.1, MaxSpeedMph: 70, AvgSpeedMph: 40,
  EnergyUsedKWh: 10, Source: "live",
});
const charge = (id: string, at: string) => ({
  ID: id, VehicleID: VEHICLE_ID, StartedAt: at, EndedAt: new Date(new Date(at).getTime() + 3_600_000).toISOString(), StartSoCPct: 30, EndSoCPct: 80,
  EnergyAddedKWh: 40, MilesAdded: 120, MaxPowerKW: 11, AvgPowerKW: 10, FinalState: "complete", Lat: 40, Lon: -100, Source: "live", Cost: 6, Currency: "USD", PricePerKWh: 0.15,
});

// Activity opens on the last 30 days for a device with no saved choice; these tests are about the Month choice, so they save it.
const MONTH = { kind: "month", offset: 0 };

async function open(page: Page, now: Date, path: string, rows: { drives: unknown[]; charges: unknown[] }, saved?: unknown) {
  await stubApi(page);
  if (saved) await page.addInitScript((p) => window.localStorage.setItem("rivolt.period", JSON.stringify(p)), saved);
  await page.clock.setFixedTime(now);
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  await page.route(/\/api\/drives(\?|$)/, (r) => r.fulfill(j(rows.drives)));
  await page.route(/\/api\/charges(\?|$)/, (r) => r.fulfill(j(rows.charges)));
  await page.goto(path);
}

const OCT = { drives: [drive("d-oct-13", day(2026, 9, 13)), drive("d-oct-28", day(2026, 9, 28))], charges: [charge("c-oct-20", day(2026, 9, 20))] };

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`Now on Nov 2 with only October recorded: the last 30 days, labelled · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, new Date(2026, 10, 2, 9, 0, 0), "/", OCT);
    const note = page.locator("[data-early-month]");
    await expect(note).toContainText("Nothing recorded yet this month, so this shows the last 30 days.");
    // The tiles count the two October drives (60 mi), not an empty November.
    await expect(page.locator('[data-ui-grid="now"]')).toContainText(/Miles\s*60\.0\s*mi/);
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SHOTS, `early-month-now-${v.n}-dark.png`) });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".maplibregl-map").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}

test("Activity on Nov 2 shows October's rows with the same note", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, new Date(2026, 10, 2, 9, 0, 0), "/activity", OCT, MONTH);
  await expect(page.locator("[data-early-month]")).toContainText("this shows the last 30 days");
  await expect(page.getByText("Nothing recorded in this period")).toHaveCount(0);
  await ctx.close();
});

test("no note once the month has data, or when another period is chosen, or when the last 30 days are empty too", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  // November already has a drive.
  const a = await ctx.newPage();
  await open(a, new Date(2026, 10, 20, 9, 0, 0), "/", { ...OCT, drives: [...OCT.drives, drive("d-nov-5", day(2026, 10, 5))] });
  await a.waitForTimeout(800);
  await expect(a.locator("[data-early-month]")).toHaveCount(0);
  // Nothing in the last 30 days either: the page stays as it is.
  const b = await ctx.newPage();
  await open(b, new Date(2026, 11, 20, 9, 0, 0), "/activity", OCT, MONTH);
  await b.waitForTimeout(800);
  await expect(b.locator("[data-early-month]")).toHaveCount(0);
  await expect(b.getByText(/No drives or charges in Dec/)).toBeVisible();
  await ctx.close();
});

test("a brand-new account on Activity gets the new-account sentence; the tiles carry the Last 30 days tag on Now", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const a = await ctx.newPage();
  await open(a, new Date(2026, 10, 2, 9, 0, 0), "/activity", { drives: [], charges: [] });
  await expect(a.getByText("Nothing recorded yet. Drives and charges appear here as you use the truck.")).toBeVisible();
  const b = await ctx.newPage();
  await open(b, new Date(2026, 10, 2, 9, 0, 0), "/", OCT);
  await expect(b.locator('[data-ui-grid="now"]')).toContainText(/Last 30 days · 2 drives/);
  await ctx.close();
});

test("Activity on its first visit opens on the last 30 days, and a saved Month choice wins", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const a = await ctx.newPage();
  await open(a, new Date(2026, 10, 2, 9, 0, 0), "/activity", OCT);
  await expect(a.getByRole("button", { name: "30 days" }).first()).toHaveAttribute("aria-pressed", "true");
  // October's rows are in view with no empty-month fallback note needed.
  await expect(a.getByText("Nothing recorded in this period")).toHaveCount(0);
  const b = await ctx.newPage();
  await open(b, new Date(2026, 10, 2, 9, 0, 0), "/activity", OCT, MONTH);
  await expect(b.getByRole("button", { name: "Month" }).first()).toHaveAttribute("aria-pressed", "true");
  await ctx.close();
});

