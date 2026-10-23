import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// Plan vs actual for a saved multi-day trip: one comparison per day, each linked on its own.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const iso = (agoMs: number) => new Date(e2eNow() - agoMs).toISOString();
const metric = (m: string, unit: string, planned: number, actual: number) => ({ metric: m, unit, planned, actual, delta: actual - planned, reasons: [] });
const comparison = {
  currency: "USD",
  plan_from: "snapshot",
  leg: { started_at: iso(3 * 86_400_000), arrived_at: iso(3 * 86_400_000 - 30_000_000), miles: 330 },
  metrics: [metric("cost", "USD", 30, 27.5), metric("energy_kwh", "kWh", 90, 88), metric("dcfc_stops", "count", 1, 1), metric("arrival_soc", "pct", 22, 19), metric("duration", "min", 400, 420)],
  stops: [{ match: "same_station", planned: { name: "Sample stop A" }, actual: { network: "Sample network" } }],
};
const cand = { trip_id: "trip_1", started_at: iso(3 * 86_400_000), ended_at: iso(3 * 86_400_000 - 30_000_000), confidence: 0.9, band: "high", signals: {} };
const base = { version: 1, saved_trip_id: "st-md" };
const actual = {
  ...base,
  kind: "multiday",
  days: [
    { ...base, day: 1, status: "linked", candidates: [cand], comparison, link: { trip_id: "trip_1", confidence: 0.9, band: "high", linked_at: iso(86_400_000) } },
    { ...base, day: 2, status: "suggested", candidates: [{ ...cand, trip_id: "trip_2" }], link: null, comparison },
    { ...base, day: 3, status: "none", candidates: [], link: null, comparison: null },
  ],
};

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`multi-day plan vs actual: a block per day, each day links on its own · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/trips/saved", (r) =>
      r.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([{ id: "st-md", name: "Chicago to Denver", inputs: { origin: { lat: 41, lon: -87, label: "Chicago" }, destination: { lat: 39, lon: -104, label: "Denver" }, extra_stops: [] }, created_at: iso(5 * 86_400_000), updated_at: iso(5 * 86_400_000) }]),
      }),
    );
    await page.route("**/api/trips/saved/st-md/actual", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(actual) }));
    const links: Record<string, unknown>[] = [];
    await page.route("**/api/trips/saved/st-md/link", (r) => {
      links.push(r.request().postDataJSON());
      return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(actual.days[1]) });
    });
    await page.goto("/trips/plan");
    await page.getByRole("tab", { name: "Plan vs actual" }).click();
    for (const d of [1, 2, 3]) await expect(page.getByRole("heading", { name: `Day ${d}: plan vs actual` })).toBeVisible();
    await expect(page.getByText("Drive this day and the actuals appear here.")).toBeVisible();
    await expect(page.getByText("Actuals come from the drives and charges Rivolt recorded on that day.")).toBeVisible();
    await page.waitForTimeout(600);
    await page.screenshot({ path: path.join(SHOTS, `pva-days-${v.n}-dark.png`), fullPage: true });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    // Confirming day 2's suggestion names the day.
    await page.locator("[data-pva-day='2']").getByRole("button", { name: "Confirm" }).click();
    await expect.poll(() => links.length).toBe(1);
    expect(links[0]).toMatchObject({ trip_id: "trip_2", day: 2 });
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}
