import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// "Three things you may not have turned on" on Now: only what the account hasn't turned on, only what the
// server offers, gone when there is nothing left or when hidden.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

type Opts = { subs?: number; pushOn?: boolean; lease?: boolean; statement?: { available: boolean; enabled: boolean } | null; ua?: string };

async function open(page: Page, o: Opts = {}) {
  await stubApi(page);
  const events: { name: string; props?: Record<string, unknown> }[] = [];
  const j = (b: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(b) });
  await page.route("**/api/push/status", (r) => r.fulfill(j({ enabled: o.pushOn ?? true, subscription_count: o.subs ?? 0 })));
  await page.route(/\/api\/vehicles\/[^/]+\/lease/, (r) => r.fulfill(o.lease ? j({ term_months: 36 }) : j({}, 404)));
  await page.route("**/api/settings/statement-email", (r) =>
    r.fulfill(o.statement === null ? j({}, 404) : j({ enabled: o.statement?.enabled ?? false, available: o.statement?.available ?? true })),
  );
  await page.route("**/api/events", (r) => {
    events.push(...((r.request().postDataJSON() as { events: { name: string; props?: Record<string, unknown> }[] }).events));
    return r.fulfill({ status: 204 });
  });
  // The events switch is on for this server.
  await page.route("**/api/config", (r) =>
    r.fulfill(j({ features: { trip_planner_enabled: true, signup_enabled: true, events_enabled: true }, tiles: { url: "/api/maps/tiles/us.pmtiles", chargers_url: "/api/maps/tiles/chargers.pmtiles" }, valhalla: { path: "/api/maps/valhalla" }, impersonation: { disabled: true } })),
  );
  await page.goto("/");
  return { card: page.locator("[data-three-things]"), events };
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`all three rows, each linking to where it is turned on · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const { card, events } = await open(page);
    await expect(card).toContainText("Three things you may not have turned on");
    await expect(card.locator("[data-three-things-row='push']").getByRole("link", { name: "Turn on" })).toHaveAttribute("href", "/settings?tab=notifications");
    await expect(card.locator("[data-three-things-row='lease']").getByRole("link", { name: "Add lease" })).toHaveAttribute("href", "/settings#lease");
    await expect(card.locator("[data-three-things-row='statement']").getByRole("link", { name: "Turn on" })).toHaveAttribute("href", "/settings?tab=notifications");
    await expect.poll(() => events.filter((e) => e.name === "alert_shown")).toEqual([{ name: "alert_shown", props: { kind: "three_things" } }]);
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SHOTS, `three-things-${v.n}-dark.png`) });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    // Hiding is for good, and counted once.
    await card.getByRole("button", { name: "Hide this" }).click();
    await expect(page.locator("[data-three-things]")).toHaveCount(0);
    await expect.poll(() => events.filter((e) => e.name === "alert_dismiss")).toEqual([{ name: "alert_dismiss", props: { kind: "three_things" } }]);
    await page.reload();
    await page.waitForTimeout(800);
    await expect(page.locator("[data-three-things]")).toHaveCount(0);
    await ctx.close();
  });
}

test("what the account already has is left out, and the statement row needs the server to offer it", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const { card } = await open(page, { subs: 1, statement: { available: false, enabled: false } });
  await expect(card).toBeVisible();
  await expect(card.locator("[data-three-things-row='push']")).toHaveCount(0);
  await expect(card.locator("[data-three-things-row='statement']")).toHaveCount(0);
  await expect(card.locator("[data-three-things-row='lease']")).toHaveCount(1);
  await ctx.close();
});

test("with all three on, or push not offered, there is no card", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const { card, events } = await open(page, { subs: 1, lease: true, statement: { available: true, enabled: true } });
  await page.waitForTimeout(1200);
  await expect(card).toHaveCount(0);
  expect(events.filter((e) => e.name === "alert_shown")).toEqual([]);
  await ctx.close();
});

test("on an iPhone the notification row says to add Rivolt to the home screen first", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark", userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1" });
  const page = await ctx.newPage();
  const { card } = await open(page);
  await expect(card.locator("[data-three-things-row='push']")).toContainText("On iPhone, add Rivolt to your home screen first (Share → Add to Home Screen).");
  await ctx.close();
});
