import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// "Your first charge" on Now: three states (priced, no home rate, first drive only), then gone after a week
// or once hidden. The notification row and the install offer only appear where they can work.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const charge = (o: Record<string, unknown> = {}) => ({
  ID: "c-first", VehicleID: VEHICLE_ID, StartedAt: ago(30 * 3_600_000), EndedAt: ago(26 * 3_600_000), StartSoCPct: 20, EndSoCPct: 80,
  EnergyAddedKWh: 42.5, MilesAdded: 120, MaxPowerKW: 11, AvgPowerKW: 10, FinalState: "complete", Lat: 40, Lon: -100, Source: "live",
  Cost: 0, Currency: "USD", PricePerKWh: 0, estimated_cost: 6.38, estimated_currency: "USD", ...o,
});
const drive = (o: Record<string, unknown> = {}) => ({
  ID: "d-first", VehicleID: VEHICLE_ID, StartedAt: ago(20 * 3_600_000), EndedAt: ago(19 * 3_600_000), StartSoCPct: 80, EndSoCPct: 70,
  StartOdometerMi: 100, EndOdometerMi: 130, DistanceMi: 30, StartLat: 40, StartLon: -100, EndLat: 40.1, EndLon: -100.1, MaxSpeedMph: 70, AvgSpeedMph: 40,
  EnergyUsedKWh: 10, Source: "live", ...o,
});

async function open(page: Page, o: { charges?: unknown[]; drives?: unknown[]; rate?: number; lease?: boolean; routes?: () => Promise<void> } = {}) {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  await page.route(/\/api\/charges(\?|$)/, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(o.charges ?? [charge()]) }));
  await page.route(/\/api\/drives(\?|$)/, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(o.drives ?? [drive()]) }));
  await page.route("**/api/settings/charging/", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ home_price_per_kwh: o.rate ?? 0.15, home_currency: "USD", gas_price_per_gallon: 4, comparison_mpg: 25 }) }),
  );
  await page.route(/\/api\/vehicles\/[^/]+\/lease/, (r) => r.fulfill(o.lease ? { status: 200, contentType: "application/json", body: JSON.stringify({ term_months: 36 }) } : { status: 404, contentType: "application/json", body: "{}" }));
  await o.routes?.();
  await page.goto("/");
  return page.locator("[data-first-charge]");
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`state A: a priced first charge, with the per-mile line and the receipt · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const card = await open(page);
    await expect(card).toHaveAttribute("data-first-charge", "A");
    await expect(card).toContainText("Your first charge: 42.5 kWh, $6.38");
    await expect(card).toContainText("Priced at your home rate of $0.15/kWh. Every charge from now on is priced the same way and lands in Money.");
    await expect(card).toContainText("That charge added about 120 mi of range (estimate), about $0.05/mi.");
    await expect(card.getByRole("link", { name: "See the receipt" })).toHaveAttribute("href", "/charges/c-first");
    await expect(card.getByRole("link", { name: "Open Money" })).toHaveAttribute("href", "/money");
    await expect(card.getByRole("link", { name: "Add lease" })).toBeVisible();
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SHOTS, `first-charge-${v.n}-dark.png`) });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    // Hiding it is for good.
    await card.getByRole("button", { name: "Hide this" }).click();
    await expect(page.locator("[data-first-charge]")).toHaveCount(0);
    await page.reload();
    await page.waitForTimeout(800);
    await expect(page.locator("[data-first-charge]")).toHaveCount(0);
    await ctx.close();
  });
}

test("state B: a first charge with no home rate asks for one", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const card = await open(page, { rate: 0, charges: [charge({ estimated_cost: 0 })] });
  await expect(card).toHaveAttribute("data-first-charge", "B");
  await expect(card).toContainText("Your first charge: 42.5 kWh");
  await expect(card).toContainText("Set your home rate and this charge gets a price now; later charges are priced as they arrive.");
  await expect(card.getByRole("link", { name: "Set home rate" })).toHaveAttribute("href", "/settings?tab=charging#cost");
  await ctx.close();
});

test("state C: a first drive before any charge", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const card = await open(page, { charges: [], rate: 0 });
  await expect(card).toHaveAttribute("data-first-charge", "C");
  await expect(card).toContainText("Your first drive: 30.0 mi at 3.0 mi/kWh");
  await expect(card).toContainText("Charges appear here as the truck is plugged in; a home rate prices them automatically.");
  await expect(card.getByRole("link", { name: "See the drive" })).toHaveAttribute("href", "/drives/d-first");
  await expect(card.getByRole("link", { name: "Set home rate" })).toBeVisible();
  await ctx.close();
});

test("after a week, or with a lease already added, the card or its row is gone", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const old = await open(page, { charges: [charge({ StartedAt: ago(9 * 86_400_000) })], drives: [drive({ StartedAt: ago(9 * 86_400_000) })] });
  await page.waitForTimeout(800);
  await expect(old).toHaveCount(0);
  await ctx.close();
  const ctx2 = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page2 = await ctx2.newPage();
  const card = await open(page2, { lease: true });
  await expect(card).toBeVisible();
  await expect(card.getByRole("link", { name: "Add lease" })).toHaveCount(0);
  await ctx2.close();
});

test("on an iPhone that is not on the home screen, the notification row says how", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark", userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1" });
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    // Safari outside the home screen has no push.
    delete (window as unknown as Record<string, unknown>).PushManager;
  });
  const card = await open(page);
  await expect(card).toContainText("On iPhone, add Rivolt to your home screen first (Share → Add to Home Screen), then turn this on here.");
  await expect(card.getByRole("button", { name: "Turn on" })).toHaveCount(0);
  await ctx.close();
});

test("Turn on subscribes this device for charging-finished only, and the row goes away", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    const sub = { endpoint: "https://push.test/device-1", toJSON: () => ({ endpoint: "https://push.test/device-1", keys: { p256dh: "a", auth: "b" } }) };
    let current: typeof sub | null = null;
    const reg = { pushManager: { getSubscription: async () => current, subscribe: async () => (current = sub) } };
    Object.defineProperty(navigator.serviceWorker, "ready", { get: () => Promise.resolve(reg) });
    Object.defineProperty(Notification, "permission", { get: () => "granted" });
  });
  const posts: Record<string, unknown>[] = [];
  const card = await open(page, {
    routes: async () => {
    await page.route("**/api/push/status", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ enabled: true, subscription_count: 0 }) }));
    await page.route("**/api/push/vapid-key", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ public_key: "BAAA" }) }));
    await page.route("**/api/push/subscribe", (r) => {
      posts.push((r.request().postDataJSON() as { preferences: Record<string, unknown> }).preferences);
      return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ok: true, preferences: {} }) });
    });
    },
  });
  const row = card.locator("[data-first-charge-row='notify']");
  await expect(row).toContainText("Get a notification when charging finishes");
  await row.getByRole("button", { name: "Turn on" }).click();
  await expect.poll(() => posts.length).toBe(1);
  expect(posts[0]).toMatchObject({ on_charging_done: true, on_plug_in_reminder: false, on_anomaly: false, on_software_update: false, on_milestones: false });
  await expect(card.locator("[data-first-charge-row='notify']")).toHaveCount(0);
  await ctx.close();
});

test("where the browser offers to install, there is an Install row that asks the browser", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    window.addEventListener("DOMContentLoaded", () => {
      const e = new Event("beforeinstallprompt") as Event & { prompt: () => Promise<void>; userChoice: Promise<{ outcome: string }> };
      e.prompt = async () => {
        (window as unknown as { __prompted: boolean }).__prompted = true;
      };
      e.userChoice = Promise.resolve({ outcome: "accepted" });
      window.dispatchEvent(e);
    });
  });
  const card = await open(page);
  const row = card.locator("[data-first-charge-row='install']");
  await expect(row).toContainText("Add Rivolt to your home screen");
  await row.getByRole("button", { name: "Install" }).click();
  await expect.poll(() => page.evaluate(() => (window as unknown as { __prompted?: boolean }).__prompted === true)).toBe(true);
  await expect(card.locator("[data-first-charge-row='install']")).toHaveCount(0);
  await ctx.close();
});

test("the first-day card offers Install where the browser does, asks only on a tap, and Not now is for good", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    window.addEventListener("DOMContentLoaded", () => {
      const e = new Event("beforeinstallprompt") as Event & { prompt: () => Promise<void>; userChoice: Promise<{ outcome: string }> };
      e.prompt = async () => {
        (window as unknown as { __prompted: number }).__prompted = ((window as unknown as { __prompted?: number }).__prompted ?? 0) + 1;
      };
      e.userChoice = Promise.resolve({ outcome: "accepted" });
      window.dispatchEvent(e);
    });
  });
  // Connected, with a truck, and nothing recorded yet: the first-day card.
  await open(page, { charges: [], drives: [] });
  const day = page.locator("[data-first-day]");
  await expect(day).toBeVisible();
  const row = day.locator("[data-install-row]");
  await expect(row).toContainText("Add Rivolt to your home screen");
  // Nothing is asked until the tap.
  expect(await page.evaluate(() => (window as unknown as { __prompted?: number }).__prompted ?? 0)).toBe(0);
  await row.getByRole("button", { name: "Not now" }).click();
  await expect(day.locator("[data-install-row]")).toHaveCount(0);
  await page.reload();
  await expect(page.locator("[data-first-day]")).toBeVisible();
  await expect(page.locator("[data-install-row]")).toHaveCount(0);
  await ctx.close();
});

test("without an install offer from the browser, the first-day card has no Install row", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, { charges: [], drives: [] });
  await expect(page.locator("[data-first-day]")).toBeVisible();
  await expect(page.locator("[data-install-row]")).toHaveCount(0);
  await ctx.close();
});
