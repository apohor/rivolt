import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { DEMO_PLAN, stubApi } from "./stub";

// Recreation.gov campgrounds in the planner, on the Pasadena to Mammoth route: the suggestion card, the "Campgrounds" chip and
// legend on the map, and the overnight row with its credit. Stubbed responses only (no campground is on that route in the data).
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const CAMP_LABEL = "Tuolumne Meadows Campground (campground)";
const INPUTS = {
  origin: pt("Pasadena", 34.15, -118.14),
  destination: pt("Mammoth Lakes", 37.65, -118.97),
  extra_stops: [pt(CAMP_LABEL, 37.87, -119.35)],
  overnight_flags: [true],
  overnight_settings: [{ lat: 37.87, lon: -119.35, until: "08:00", l2_kw: 7.2, max_soc_pct: 80, hookup: "nema_14_50", campground: true }],
  max_overnight_soc_pct: 80,
  overnight_departure_local: "08:00",
  overnight_l2_kw: 7.2,
};

const day = (index: number, from: string, to: string, over?: object, long = false) => ({
  index,
  plan: long ? { ...DEMO_PLAN, Routes: [{ ...DEMO_PLAN.Routes[0], TotalDriveDurationSec: 40_000 }] } : DEMO_PLAN,
  depart_at: `2026-11-1${index}T15:00:00Z`,
  arrive_at: `2026-11-1${index}T23:00:00Z`,
  start: { latitude: 34.15, longitude: -118.14, label: from },
  end: { latitude: 37.65, longitude: -118.97, label: to },
  departure_soc: 90,
  arrival_soc: 24,
  destination_reached: true,
  soc_below_limit: false,
  ...(over ? { overnight: over } : {}),
  costs: DEMO_PLAN.costs,
});
const BODY = {
  days: [
    day(1, "Pasadena", CAMP_LABEL, { name: CAMP_LABEL, parked_hours: 11, l2_kw: 7.2, added_kwh: 52, post_charge_soc_pct: 80, capped: true, hookup: "nema_14_50" }),
    day(2, CAMP_LABEL, "Mammoth Lakes", undefined, true),
  ],
  total: { distance_meters: 500_000, drive_duration_sec: 60_000, charging_duration_sec: 6_000 },
  tz: "America/Los_Angeles",
};
const CANDIDATES = {
  candidates: [
    {
      kind: "campground", name: "Convict Lake Campground", lat: 37.59, lon: -118.86, l2_kw: 7.2, l2_kw_assumed: true, detour_m: 4000, along_m: 300000,
      connector: "nema_14_50", needs_adapter: false, electric_sites: 12, sites: 88, reservation_required: true, fee: "$30 per night",
      source: "Recreation.gov", source_url: "https://www.recreation.gov/camping/campgrounds/232447", asof: "2026-10-20",
    },
    {
      kind: "campground", name: "Lower Lee Vining <b>Camp</b>", lat: 37.94, lon: -119.12, l2_kw: 1.4, l2_kw_assumed: true, detour_m: 2000, along_m: 310000,
      connector: "nema_tt_30", needs_adapter: true, electric_sites: 5, sites: 30, reservation_required: null, fee: "",
      source: "Recreation.gov", source_url: "javascript:alert(1)", asof: "2026-10-20",
    },
    { kind: "hotel", name: "Sample Inn, Bishop", lat: 37.36, lon: -118.4, l2_kw: 19, l2_kw_assumed: false, detour_m: 6000, along_m: 320000 },
  ],
  target: { lat: 37.6, lon: -118.9, along_m: 302000, reaches_end: false },
  route_m: 500000,
  source: "listing",
  attribution: "Data: Recreation.gov (RIDB), CC BY 4.0; filtered by Rivolt",
};
const CAMP_POI = { lat: 37.59, lon: -118.86, name: "Convict Lake Campground", isDCFC: false, isL2: false, facilityType: "CAMPGROUND", hookupAmps: 50, electricSites: 12, sites: 88, ridbID: "ridb/232447", asof: "2026-10-20" };

async function open(page: Page, asked: { kinds?: string[] }[]) {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  await page.route("**/api/trips/overnight-candidates", (r) => {
    asked.push(r.request().postDataJSON());
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(CANDIDATES) });
  });
  await page.route("**/api/maps/chargers-along", (r) => {
    const f = (r.request().postDataJSON() as { filter?: string }).filter;
    const chargers = f === "camp" || f === "all" ? [CAMP_POI] : [];
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ chargers, count: chargers.length }) });
  });
  await page.route("**/api/trips/plan-multiday", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(BODY) }));
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: "st-camp", name: "Pasadena to Mammoth", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]) }),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Pasadena to Mammoth/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan 2-day trip" }).click();
  await expect(page.locator("[data-overnight-row]")).toBeVisible();
}

for (const v of [
  { w: 390, h: 844, scheme: "dark" as const },
  { w: 1280, h: 900, scheme: "light" as const },
]) {
  test(`campgrounds on Pasadena to Mammoth: card, row, credit, chip · ${v.w}px ${v.scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: v.w, height: v.h }, colorScheme: v.scheme, timezoneId: "America/Los_Angeles" });
    const page = await ctx.newPage();
    const asked: { kinds?: string[] }[] = [];
    await open(page, asked);

    // The overnight row: the campground, its outlet and kW as an estimate, the warning and the credit.
    const row = page.locator("[data-overnight-row]");
    await expect(row).toContainText("Overnight at Tuolumne Meadows Campground (campground)");
    await expect(row.locator("[data-stay-line-camp]")).toHaveText("Campground overnight, 50 A hookup (NEMA 14-50), about 7.2 kW (estimate)");
    await expect(row.locator("[data-stay-warning]")).toHaveText("Reservation required. Listed, not guaranteed. Some parks don't allow EV charging; ask first.");
    await expect(row.locator("[data-stay-note]")).toContainText("Rivian's Portable Charger");

    // The suggestions: both campgrounds, the hotel, the guarded links and the credit.
    const card = page.locator("[data-overnight-suggestions]");
    await expect(card).toContainText("these stays list a Level 2 charger or an electric hookup:");
    expect(asked[0].kinds).toEqual(["hotel", "campground"]);
    const camps = card.locator('[data-stay-card="campground"]');
    await expect(camps).toHaveCount(2);
    await expect(camps.first()).toContainText("Convict Lake Campground");
    await expect(camps.first()).toContainText("12 of 88 sites have power");
    await expect(camps.first()).toContainText("About 7.2 kW (estimate) on a 50 A site with Rivian's Portable Charger.");
    await expect(camps.first().getByRole("link", { name: "on Recreation.gov" })).toHaveAttribute("href", "https://www.recreation.gov/camping/campgrounds/232447");
    await expect(camps.first()).toContainText("Fee: $30 per night");
    // A TT-30 site needs an adapter and plans slow; an unsafe link is not drawn; markup in a name is text.
    await expect(camps.nth(1)).toContainText("Lower Lee Vining <b>Camp</b>");
    await expect(camps.nth(1)).toContainText("needs an adapter not included with the truck");
    await expect(camps.nth(1).getByRole("link")).toHaveCount(0);
    await expect(card.locator('[data-stay-card="hotel"]')).toContainText("up to 19 kW");
    await expect(card.locator("[data-ridb-credit]")).toHaveText("Data: Recreation.gov (RIDB), CC BY 4.0; filtered by Rivolt");
    await expect(card.locator("[data-ridb-credit] a")).toHaveCount(2);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include("[data-overnight-suggestions]").include("[data-overnight-row]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await card.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `campground-${v.w}-${v.scheme}.png`), fullPage: true });

    // The map: the Campgrounds chip is there because the route has one, with its legend.
    const chips = page.locator("[data-map-chips]").first();
    await expect(chips.getByRole("button", { name: "Campgrounds" })).toBeVisible();
    await chips.getByRole("button", { name: "Campgrounds" }).click();
    await expect(chips).toContainText("Campground, electric hookup");
    await ctx.close();
  });
}

test("using a campground makes it the night with its outlet and sends the hookup", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark", timezoneId: "America/Los_Angeles" });
  const page = await ctx.newPage();
  const reqs: { days: { overnight?: { hookup?: string; l2_kw: number; name: string } }[] }[] = [];
  await open(page, []);
  await page.route("**/api/trips/plan-multiday", (r) => {
    reqs.push(r.request().postDataJSON());
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(BODY) });
  });
  await page.getByRole("button", { name: "Use Convict Lake Campground (campground) as the overnight stop" }).click();
  const night = page.locator("[data-overnight-stop]").filter({ hasText: "Convict Lake Campground (campground)" });
  await expect(night.getByLabel("Hookup at the stop")).toHaveValue("nema_14_50");
  await expect(night.getByLabel("Planned power in kW")).toHaveValue("7.2");
  await page.getByRole("button", { name: /Plan \d-day trip/ }).click();
  await expect.poll(() => reqs.length).toBe(1);
  const sent = reqs[0].days.map((d) => d.overnight).filter(Boolean);
  expect(sent).toContainEqual(expect.objectContaining({ hookup: "nema_14_50", l2_kw: 7.2, name: "Convict Lake Campground (campground)" }));
  await ctx.close();
});
