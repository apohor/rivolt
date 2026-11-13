import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Shared trip links. The sheet on a saved trip says what the link shows, makes one and lists it; the public page /s#<token> opens it with no
// account, takes the token out of the address at once, sends it only in a POST body, and draws the route's segments as separate lines.
const TOKEN = "abcdefghijklmnopqrstuv";
const j = (b: unknown, status = 200, headers: Record<string, string> = {}) => ({ status, contentType: "application/json", headers, body: JSON.stringify(b) });
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };

// Two polyline6 segments with a gap between them (the server cut a part out).
const enc = (pts: [number, number][]) => {
  let la = 0, lo = 0, out = "";
  const put = (v: number) => {
    let n = v < 0 ? ~(v << 1) : v << 1;
    while (n >= 0x20) { out += String.fromCharCode((0x20 | (n & 0x1f)) + 63); n >>= 5; }
    out += String.fromCharCode(n + 63);
  };
  for (const [a, b] of pts) {
    const ia = Math.round(a * 1e6), ib = Math.round(b * 1e6);
    put(ia - la); put(ib - lo); la = ia; lo = ib;
  }
  return out;
};
const SNAPSHOT = {
  version: 1,
  title: "Weekend trip",
  planned_at: "2026-11-13T10:00:00Z",
  depart_at: "2026-11-14T16:00:00Z",
  tz: "America/Los_Angeles",
  start_soc: 90,
  totals: { distance_m: 424000, drive_s: 13570, charge_s: 1093, arrival_soc: 30.1 },
  start: { label: "Start area", lat: 34.05, lon: -118.25 },
  end: { label: "Destination area", lat: 36.15, lon: -115.15 },
  stops: [
    { kind: "charger", name: "Example Fast Charge, CA", lat: 34.9, lon: -117.0, at_m: 164000, arrive_at: "2026-11-14T18:40:00Z", depart_at: "2026-11-14T19:00:00Z", arrive_soc: 58.4, depart_soc: 80, charge_s: 1093, max_kw: 250 },
    { kind: "stop", lat: 35.5, lon: -116.0, at_m: 300000, arrive_soc: 45, depart_soc: 45 },
  ],
  route: { segments: [enc([[34.1, -118.1], [34.5, -117.5], [34.9, -117.0]]), enc([[35.3, -116.4], [35.7, -115.8], [36.1, -115.2]])] },
  elevation_profile: { points: [[0, 100], [100000, 900], [200000, 600], [400000, 700]], waypoint_at_m: [0, 164000] },
};

async function openShared(page: Page, answer: ReturnType<typeof j>) {
  await stubApi(page, { signedIn: false });
  const bodies: string[] = [];
  const urls: string[] = [];
  await page.route("**/api/public/trips/share", (r) => {
    bodies.push(r.request().postData() ?? "");
    urls.push(r.request().url());
    return r.fulfill(answer);
  });
  await page.goto(`/s#${TOKEN}`);
  return { bodies, urls };
}

for (const [name, size] of [["phone", { width: 393, height: 852 }], ["desktop", { width: 1280, height: 900 }]] as const) {
  test(`the shared trip page opens a link with no account and clears the token (${name})`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: size, colorScheme: "dark" });
    const page = await ctx.newPage();
    const calls: string[] = [];
    page.on("request", (r) => calls.push(r.method() + " " + r.url()));
    const { bodies, urls } = await openShared(page, j(SNAPSHOT));
    await expect(page.getByRole("heading", { name: "Weekend trip" })).toBeVisible();
    // The token: gone from the address bar, in the POST body only.
    expect(await page.evaluate(() => location.hash + location.href)).not.toContain(TOKEN);
    expect(bodies).toEqual([JSON.stringify({ token: TOKEN })]);
    expect(urls.join()).not.toContain(TOKEN);
    expect(calls.join("\n")).not.toContain(TOKEN);
    // No sign-in probe, no analytics: the page asks only for config and the trip.
    expect(calls.some((c) => c.includes("/api/auth/me") || c.includes("/api/events"))).toBe(false);
    // Two separate lines (not joined across the cut), one amber charger and one grey stop.
    await expect(page.locator("[data-testid=shared-trip-map] path[data-segment]")).toHaveCount(2);
    await expect(page.locator("[data-stop=charger]")).toHaveCount(1);
    await expect(page.locator("[data-stop=stop]")).toHaveCount(1);
    await expect(page.getByText("Example Fast Charge, CA")).toBeVisible();
    // Times are in the trip's own zone: 16:00Z is 8:00 AM in Los Angeles.
    await expect(page.getByText(/Leaves .*8:00\s?AM/)).toBeVisible();
    await expect(page.getByText(/approximate/)).toBeVisible();
    // Phone: nothing sticks out sideways.
    const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(over).toBeLessThanOrEqual(0);
    await page.screenshot({ path: `e2e/__screenshots__/trip-share-page-${size.width}.png`, fullPage: true });
    await ctx.close();
  });
}

test("a link that is off, a busy server and a rate limit each say so, and Try again asks again", async ({ page }) => {
  await openShared(page, j({ error: "not found" }, 404));
  await expect(page.getByRole("heading", { name: "This link isn't available" })).toBeVisible();

  const page2 = await page.context().newPage();
  await stubApi(page2, { signedIn: false });
  let n = 0;
  await page2.route("**/api/public/trips/share", (r) => (++n === 1 ? r.fulfill(j({ error: "busy" }, 503, { "Retry-After": "2" })) : r.fulfill(j(SNAPSHOT))));
  await page2.goto(`/s#${TOKEN}`);
  await expect(page2.getByRole("heading", { name: "Rivolt is busy" })).toBeVisible();
  await page2.getByRole("button", { name: "Try again" }).click();
  await expect(page2.getByRole("heading", { name: "Weekend trip" })).toBeVisible();
  expect(n).toBe(2);

  const page3 = await page.context().newPage();
  await stubApi(page3, { signedIn: false });
  await page3.route("**/api/public/trips/share", (r) => r.fulfill(j({ error: "slow down" }, 429, { "Retry-After": "30" })));
  await page3.goto(`/s#${TOKEN}`);
  await expect(page3.getByRole("heading", { name: "Too many requests" })).toBeVisible();
});

test("a link with no token says it is not available", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.goto("/s");
  await expect(page.getByRole("heading", { name: "This link isn't available" })).toBeVisible();
});

async function savedTrip(page: Page, tripShare: boolean) {
  await stubApi(page, { tripShare });
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Home to Denver", inputs: INPUTS, plan: DEMO_PLAN, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
}

test("Share on a saved trip says what the link shows, makes one, shows it once and turns it off", async ({ page }) => {
  await savedTrip(page, true);
  let live = [{ id: "sh-1", saved_trip_id: "st-a", title: "Older link", created_at: "2026-11-01T00:00:00Z", expires_at: null, revoked_at: null }];
  let created: { title?: string; expires_in_days?: number } | null = null;
  await page.route("**/api/trips/saved/st-a/shares", (r) => r.fulfill(j({ links: live })));
  await page.route("**/api/trips/saved/st-a/share", (r) => {
    created = r.request().postDataJSON();
    const l = { id: "sh-2", saved_trip_id: "st-a", title: created?.title ?? "", created_at: "2026-11-13T10:00:00Z", expires_at: "2026-12-13T10:00:00Z", revoked_at: null, token: TOKEN, path: `/s#${TOKEN}` };
    live = [...live, l];
    return r.fulfill(j(l, 201));
  });
  const revoked: string[] = [];
  await page.route("**/api/trips/shares/*", (r) => {
    revoked.push(r.request().url().split("/").pop()!);
    live = live.map((l) => (l.id === revoked[revoked.length - 1] ? { ...l, revoked_at: "2026-11-13T11:00:00Z" } : l));
    return r.fulfill({ status: 204 });
  });
  await page.getByTestId("trip-share-open").click();
  const sheet = page.getByTestId("trip-share-sheet");
  // Q3: the sheet says the link shows the start area and the departure date and time.
  await expect(sheet.getByTestId("trip-share-says")).toContainText(/start area/);
  await expect(sheet.getByTestId("trip-share-says")).toContainText(/departure date and time/);
  await expect(sheet.getByText("Older link")).toBeVisible();
  await sheet.getByLabel(/Title on the link/).fill("Weekend trip");
  await sheet.getByRole("radio", { name: "7 days" }).check();
  await sheet.getByRole("button", { name: "Create link" }).click();
  await expect(sheet.getByTestId("trip-share-made")).toBeVisible();
  expect(created).toEqual({ title: "Weekend trip", expires_in_days: 7 });
  await expect(sheet.getByLabel(/Your link/)).toHaveValue(new RegExp(`/s#${TOKEN}$`));
  await sheet.getByRole("button", { name: /Turn off the link "Older link"/ }).click();
  await expect.poll(() => revoked).toEqual(["sh-1"]);
  await expect(sheet.getByText("Turned off")).toBeVisible();
  await page.screenshot({ path: "e2e/__screenshots__/trip-share-sheet.png" });
  await page.keyboard.press("Escape");
  await expect(sheet).toBeHidden();
});

test("a create that fails says why in words", async ({ page }) => {
  await savedTrip(page, true);
  await page.route("**/api/trips/saved/st-a/shares", (r) => r.fulfill(j({ links: [] })));
  await page.route("**/api/trips/saved/st-a/share", (r) => r.fulfill(j({ code: "too_short", error: "too short" }, 422)));
  await page.getByTestId("trip-share-open").click();
  await page.getByRole("button", { name: "Create link" }).click();
  await expect(page.getByTestId("trip-share-error")).toContainText("too short to share");
});

test("without the trip-share flag a saved trip has no Share", async ({ page }) => {
  await savedTrip(page, false);
  await expect(page.getByRole("button", { name: /Home to Denver/ })).toBeVisible();
  await expect(page.getByTestId("trip-share-open")).toHaveCount(0);
});
