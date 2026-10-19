import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

// The public sample-trip pages read the plan the server puts in the page as JSON: no API call, a phone layout
// with no sideways scroll, and plain-anchor links so the next page gets its own JSON.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const TRIP = {
  slug: "la-las-vegas-two-days",
  title: "LA to Las Vegas and back in a Rivian: a two-day sample plan",
  intro: "Two days, 530 miles, from Pasadena to Las Vegas and home again.",
  demo_url: "https://demo.rivolt.dev/trips/plan?preset=vegas",
  signup_url: "/signup",
  currency: "USD",
  totals: { distance_mi: 529.9, drive_min: 480, charge_min: 70, charge_stops: 2, dcfc_cost_est: 48.2 },
  days: [
    {
      label: "Day 1", depart_at: "2026-11-02T08:00:00", arrive_at: "2026-11-02T15:00:00", from: "Pasadena", to: "Las Vegas hotel", distance_mi: 270, drive_min: 240, arrival_soc: 21,
      stops: [{ name: "Barstow RAN site", network: "Rivian Adventure Network", arrive_soc: 24, depart_soc: 80, charge_min: 35, max_kw: 200, cost_est: 24.1 }],
      overnight: { name: "Sample hotel", parked_hours: 12, l2_kw: 6, added_kwh: 79, soc_after: 100 },
    },
    {
      label: "Day 2", depart_at: "2026-11-03T08:00:00", arrive_at: "2026-11-03T15:00:00", from: "Las Vegas hotel", to: "Pasadena", distance_mi: 260, drive_min: 240, arrival_soc: 18,
      stops: [{ name: "Primm", network: "Rivian Adventure Network", arrive_soc: 70, depart_soc: 85, charge_min: 12, max_kw: 150, cost_est: 8 }],
      overnight: null,
    },
  ],
  route: [[34.15, -118.14], [34.9, -117.0], [35.6, -115.5], [36.17, -115.14]],
};
const HUB = { trips: [{ slug: TRIP.slug, title: TRIP.title, intro: TRIP.intro, totals: TRIP.totals }] };

async function open(page: Page, url: string, id: string, data: unknown | null) {
  const apiCalls: string[] = [];
  page.on("request", (r) => {
    const p = new URL(r.url()).pathname;
    if (p.startsWith("/api/")) apiCalls.push(p);
  });
  // The app shell asks who is signed in and for its config on every public page; none of that is the page's own.
  await page.route(/\/api\/(config|health|auth\/me)$/, (r) => r.fulfill({ status: r.request().url().endsWith("/me") ? 401 : 200, contentType: "application/json", body: "{}" }));
  if (data) {
    await page.addInitScript(([i, d]) => {
      document.addEventListener("DOMContentLoaded", () => {
        const s = document.createElement("script");
        s.type = "application/json";
        s.id = i;
        s.textContent = d;
        document.body.appendChild(s);
      });
    }, [id, JSON.stringify(data)] as const);
  }
  await page.goto(url);
  return apiCalls;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`sample trip page renders from its JSON · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const calls = await open(page, `/sample-trips/${TRIP.slug}`, "sample-trip", TRIP);
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(TRIP.title);
    await expect(page.getByText("Barstow RAN site")).toBeVisible();
    await expect(page.getByText(/Overnight at Sample hotel/)).toBeVisible();
    await expect(page.getByRole("img", { name: /Route of/ })).toBeVisible();
    await expect(page.getByRole("link", { name: "Open in the demo planner" })).toHaveAttribute("href", TRIP.demo_url);
    await expect(page.getByRole("link", { name: "Plan your own" })).toHaveAttribute("href", "/signup");
    await expect(page.getByText("Unofficial, not affiliated with Rivian.")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `sample-trip-${v.n}-dark.png`), fullPage: true });
    expect(calls.filter((c) => !/^\/api\/(health|config|auth\/me)$/.test(c))).toEqual([]);
    await ctx.close();
  });
}

test("the hub lists the trips with plain links; a page without its JSON says so", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const a = await ctx.newPage();
  await open(a, "/sample-trips", "sample-trips", HUB);
  await expect(a.getByRole("link", { name: TRIP.title })).toHaveAttribute("href", `/sample-trips/${TRIP.slug}`);
  const b = await ctx.newPage();
  await open(b, `/sample-trips/${TRIP.slug}`, "sample-trip", null);
  await expect(b.getByRole("heading", { name: /didn't load/ })).toBeVisible();
  await expect(b.getByRole("link", { name: "list of sample trips" })).toHaveAttribute("href", "/sample-trips");
  await ctx.close();
});
