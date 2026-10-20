import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

// The public sample-trip pages read the plan the server puts in the page as JSON: no API call, a phone layout
// with no sideways scroll, and plain-anchor links so the next page gets its own JSON.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

// The exact JSON the server emits (architect's generated stubs).
const read = (f: string) => JSON.parse(fs.readFileSync(path.join(process.cwd(), "e2e", "fixtures", f), "utf8"));
const TRIP = read("sample-trip.json");
const HUB = read("sample-trips-hub.json");

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
    await expect(page.getByText(/Barstow, CA/).first()).toBeVisible();
    await expect(page.getByText(/Overnight at Las Vegas hotel.*2 nights, parked about 45 h/)).toBeVisible();
    await expect(page.getByRole("img", { name: /Route of/ })).toBeVisible();
    // The fast-charging cost is a range over the priced stops; a stop with no price says so (never $0).
    await expect(page.getByText(/about \$[\d.]+–\$[\d.]+/).first()).toBeVisible();
    await expect(page.getByText(/1 of 2 stops priced/)).toBeVisible();
    // The total names its source and month, and "DCFC Tracker" is a link to the tracker's own site (never anywhere else).
    await expect(page.getByText(/estimate, typical price seen by DCFC Tracker, Oct 2026/).first()).toBeVisible();
    const links = page.getByRole("link", { name: "DCFC Tracker" });
    expect(await links.count()).toBeGreaterThanOrEqual(2);
    for (const h of await links.evaluateAll((els) => els.map((e) => (e as HTMLAnchorElement).href))) expect(h).toMatch(/^https:\/\/dcfctracker\.com\//);
    await expect(page.getByText("· price shown at the charger").first()).toBeVisible();
    await expect(page.getByText("Typical price seen by DCFC Tracker, Oct 2026. The price at the charger may differ.").first()).toBeVisible();
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

test("without a price month, a cost is never shown as a dateless 'typical price'", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const undated = JSON.parse(JSON.stringify(TRIP));
  delete undated.totals.price_month;
  for (const d of undated.days) for (const st of d.stops) delete st.price_month;
  await open(page, `/sample-trips/${TRIP.slug}`, "sample-trip", undated);
  await expect(page.getByText("Price shown at the charger").first()).toBeVisible();
  await expect(page.getByText(/typical price seen by/)).toHaveCount(0);
  await expect(page.getByText(/about \$[\d.]+/)).toHaveCount(0);
  await ctx.close();
});
