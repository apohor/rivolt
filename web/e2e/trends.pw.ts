import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// Truck tab "Trends for your truck": charts only when the server's rule is met, honest sentences otherwise,
// and "can't be tracked" where the truck has no capacity signal.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const months = ["2025-12", "2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09", "2026-10"];
const eff = months.map((m, i) => ({ month: m, distance: i === 2 ? 20 : 300 + i * 10, energy_kwh: 100, efficiency: i === 2 ? 2.1 : 2.6 + (i % 4) * 0.15, drives: 14 }));
const samples = Array.from({ length: 9 }, (_, i) => ({ at: new Date(2026, 1 + i, 10).toISOString(), kwh_effective: 124 + (i % 3) * 0.8 }));
const base = { units: "imperial", distance_unit: "mi", efficiency_unit: "mi/kWh", timezone: "America/Chicago", months: 12, min_month_distance: 50, min_months: 3 };
const FULL = { ...base, qualifying_months: 10, efficiency: eff, capacity: { samples, median_kwh: 124.8, spec_kwh: 131, reported_kwh: 129, qualifying_count: 9, span_days: 240, min_samples: 5, min_span_days: 30 } };
const SPARSE = { ...base, qualifying_months: 1, efficiency: eff.slice(-1), capacity: { samples: samples.slice(0, 2), median_kwh: null, spec_kwh: 131, reported_kwh: 129, qualifying_count: 2, span_days: 3, min_samples: 5, min_span_days: 30 } };
const R2 = { ...base, qualifying_months: 4, efficiency: eff.slice(-4), capacity: null };

// Shaped like a real history: a January cluster, one May charge, a September–October cluster; estimates scattered
// 125–145 kWh around ~138, from top-ups (small energy) to big charges. Synthetic values.
const SPREAD_ROWS: [string, number, number][] = [
  ["2026-01-03", 141.2, 72], ["2026-01-06", 134.8, 24], ["2026-01-09", 139.5, 88], ["2026-01-13", 145.3, 19], ["2026-01-16", 137.9, 65],
  ["2026-01-20", 136.4, 41], ["2026-01-24", 140.1, 79], ["2026-01-28", 128.2, 17],
  ["2026-05-14", 126.9, 33],
  ["2026-09-12", 138.6, 81], ["2026-09-15", 144.7, 22], ["2026-09-19", 137.2, 70], ["2026-09-23", 135.1, 46], ["2026-09-27", 139.8, 90],
  ["2026-10-01", 125.4, 15], ["2026-10-05", 138.9, 76], ["2026-10-09", 141.6, 58], ["2026-10-13", 136.7, 85], ["2026-10-16", 143.9, 21],
  ["2026-10-20", 138.3, 68],
];
const SPREAD = {
  ...base, qualifying_months: 10, efficiency: eff,
  capacity: {
    samples: SPREAD_ROWS.map(([d, k, e]) => ({ at: `${d}T08:00:00Z`, kwh_effective: k, energy_kwh: e, soc_delta_pct: Math.round((e / k) * 100) })),
    median_kwh: 138.4, spec_kwh: 141, reported_kwh: 139, qualifying_count: 20, span_days: 290, min_samples: 5, min_span_days: 30,
  },
};

async function open(page: Page, body: unknown, status = 200) {
  await stubApi(page);
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/trends`), (r) => r.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) }));
  await page.goto("/truck");
  await expect(page.getByText("Battery capacity").first()).toBeVisible();
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`trends with enough history draw both charts · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, FULL);
    const card = page.locator("[data-trends]");
    await expect(card.getByRole("img", { name: /Efficiency by month/ })).toBeVisible();
    // One capacity card: the truck's own figure first, our estimate on one labelled line under it.
    const cap = page.locator("[data-battery-capacity]");
    await expect(cap.getByRole("img", { name: /Estimated usable capacity/ })).toBeVisible();
    await expect(cap.locator("[data-cap-head]")).toContainText("129.0 kWh");
    await expect(cap.locator("[data-cap-head]")).toContainText("usable, as your truck reports it");
    await expect(cap.locator("[data-cap-estimate]")).toHaveText("Our estimate from your charges: about 125 kWh, from 9 charges.");
    await expect(cap).toContainText("Rivian's spec for this pack: 135 kWh");
    await expect(card).not.toContainText(/capacity/i);
    for (const t of [card, cap]) await expect(t).not.toContainText(/%\s*of\s*(nameplate|spec)|degradation|battery health/i);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await card.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `trends-${v.n}-dark.png`), fullPage: true });
    await ctx.close();
  });
}

test("sparse history says how far along it is, with no charts", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, SPARSE);
  const card = page.locator("[data-trends]");
  await expect(card).toContainText("1 of 3 months with at least 50 mi");
  const cap = page.locator("[data-battery-capacity]");
  await expect(cap).toContainText("Our estimate needs about 5 charges that add at least 30% of battery, over at least 30 days. So far: 2 of 5 charges, over 3 of 30 days.");
  await expect(card.getByRole("img")).toHaveCount(0);
  await expect(cap.getByRole("img")).toHaveCount(0);
  // The truck's own figure still leads; there is no estimate line yet.
  await expect(cap.locator("[data-cap-head]")).toContainText("usable, as your truck reports it");
  await expect(cap.locator("[data-cap-estimate]")).toHaveCount(0);
  await page.screenshot({ path: path.join(SHOTS, "trends-sparse-iphone-dark.png"), fullPage: true });
  await ctx.close();
});

test("enough charges but too short a span still says what is missing", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, { ...SPARSE, capacity: { ...SPARSE.capacity, qualifying_count: 5, span_days: 12 } });
  await expect(page.locator("[data-battery-capacity]")).toContainText("So far: 5 of 5 charges, over 12 of 30 days.");
  await ctx.close();
});

test("a met span isn't shown as a shortfall (no '45 of 30 days')", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, { ...SPARSE, capacity: { ...SPARSE.capacity, qualifying_count: 4, span_days: 45 } });
  const card = page.locator("[data-battery-capacity]");
  await expect(card).toContainText("So far: 4 of 5 charges.");
  await expect(card).not.toContainText("45 of 30");
  await ctx.close();
});

test("a truck with no capacity signal says so and still shows efficiency", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, R2);
  const card = page.locator("[data-trends]");
  await expect(page.locator("[data-battery-capacity]")).toContainText("Our estimate can't be tracked on this truck yet.");
  await expect(card.getByRole("img", { name: /Efficiency by month/ })).toBeVisible();
  await ctx.close();
});

test("a server without the endpoint shows no card; an error offers a retry", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const a = await ctx.newPage();
  await open(a, { error: "not found" }, 404);
  await a.waitForTimeout(800);
  await expect(a.locator("[data-trends]")).toHaveCount(0);
  const b = await ctx.newPage();
  await open(b, { error: "boom" }, 500);
  await expect(b.getByRole("button", { name: "Try again" })).toBeVisible();
  await ctx.close();
});

// The owner's redesign: dots (no line across empty months), weighted by energy, a rolling median only where dense,
// the median ±15 %. Renders for review in light and dark.
for (const scheme of ["light", "dark"] as const) {
  for (const v of [
    { n: "iphone", vp: { width: 390, height: 844 } },
    { n: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    test(`capacity estimates as dots · ${v.n} · ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme });
      await ctx.addInitScript((t) => localStorage.setItem("rivolt.theme", t), scheme);
      const page = await ctx.newPage();
      await open(page, SPREAD);
      const card = page.locator("[data-battery-capacity]");
      await expect(card.locator("[data-cap-estimate]")).toHaveText("Our estimate from your charges: about 138 kWh, from 20 charges.");
      await expect(card.locator("[data-cap-dot]")).toHaveCount(20);
      // Two dense runs (January, September–October), none across the empty months or through the lone May charge.
      await expect(card.locator("[data-cap-trend]")).toHaveCount(2);
      await expect(card.locator("[data-cap-spec]")).toHaveCount(1);
      await expect(card).not.toContainText(/degradation|battery health|measured/i);
      const svg = card.locator("svg[role='img']");
      const box = await svg.boundingBox();
      // Tap the first dot: its date and estimate. (Centre the chart first: on a phone the tab bar covers the bottom.)
      await svg.evaluate((e) => e.scrollIntoView({ block: "center" }));
      const first = card.locator("[data-cap-dot]").first();
      const c = await first.boundingBox();
      await page.mouse.click(c!.x + c!.width / 2, c!.y + c!.height / 2);
      await expect(card.locator("[data-cap-tip]")).toContainText(/about \d+ kWh \(estimate\)/);
      expect(box!.width).toBeGreaterThan(200);
      await card.scrollIntoViewIfNeeded();
      await card.screenshot({ path: path.join(SHOTS, `capacity-dots-${v.n}-${scheme}.png`) });
      await ctx.close();
    });
  }
}

// Without the truck's own figure, our estimate is the headline, called an estimate.
test("no figure from the truck: our estimate leads, labelled as one", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  // After the stub: the last route registered wins.
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/trends`), (r) => r.fulfill({ json: SPREAD }));
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/pack-health`), (r) =>
    r.fulfill({ json: { samples: [], headline: { effective_kwh: 0, nameplate_kwh: 0, pct_of_nameplate: 0, documented_kwh: 141, reported_kwh: 0, reported_pct_of_documented: 0, sample_count: 0, window: 0 } } }),
  );
  await page.goto("/truck");
  const cap = page.locator("[data-battery-capacity]");
  await expect(cap.locator("[data-cap-head]")).toContainText("About 138 kWh");
  await expect(cap.locator("[data-cap-head]")).toContainText("usable (estimate)");
  await expect(cap.locator("[data-cap-count]")).toHaveText("from 20 charges");
  await expect(cap.locator("[data-cap-estimate]")).toHaveCount(0);
  await ctx.close();
});
