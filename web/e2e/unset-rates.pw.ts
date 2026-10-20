import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";
import { currentMonth, ledgerMonthFixture, ledgerMonthFromReceipts, ledgerSummaryFixture } from "../src/lib/ledger";

// The seeded network rates count as unset: Settings shows them blank with a note, "Save X/kWh" only when the user entered
// both prices, and the one-click apply on a charge waits for the user's own rate.
const NETWORKS = [
  { name: "Tesla Supercharger", price_per_kwh: 0, member_price_per_kwh: 0, member_plan: "Supercharging Membership", currency: "USD", slug: "tesla_sc" },
  { name: "Electrify America", price_per_kwh: 0.52, member_price_per_kwh: 0.4, currency: "USD", slug: "ea" },
  { name: "EVgo", price_per_kwh: 0, currency: "USD", slug: "evgo" },
];

async function setup(page: Page) {
  await stubApi(page);
  await page.route("**/api/settings/charging/networks", (r) => r.fulfill({ json: NETWORKS }));
  const charge = (n: number, o: Record<string, unknown>) => ({
    ID: `charge-${n}`, VehicleID: VEHICLE_ID, StartedAt: new Date(e2eNow() - 2 * 86_400_000).toISOString(), EndedAt: new Date(e2eNow() - 2 * 86_400_000 + 1_800_000).toISOString(),
    StartSoCPct: 20, EndSoCPct: 80, EnergyAddedKWh: 52.4, MilesAdded: 150, MaxPowerKW: 180, AvgPowerKW: 110, FinalState: "charging_complete", Lat: 40.9, Lon: -100.9, Source: "live", Currency: "USD", ...o,
  });
  await page.route("**/api/charges?**", (r) => r.fulfill({ json: [charge(1, { Cost: 20.96, PricePerKWh: 0.4, CostSource: "network_estimate", network_state: "needs_confirmation", network_confidence: 0.6, network_evidence: { v: 1, source: "archive", candidates: [{ network: "Tesla Supercharger", slug: "tesla_sc", dist_m: 38 }], pos: "open", rate_basis: "guest" } })] }));
}

for (const w of [390, 1280] as const) {
  test(`Settings: unset rows are blank with a note; the saving shows only when both prices are entered · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await setup(page);
    await page.goto("/settings?tab=charging");
    const card = page.locator("#networks");
    await expect(card.locator("[data-rate-unset]")).toHaveCount(2);
    await expect(card.locator("[data-rate-unset]").first()).toHaveText("Enter the rate you pay. Prices vary by site.");
    // Blank inputs, not seeded numbers.
    const tesla = card.locator("input[placeholder='Network name']").nth(0);
    await expect(tesla).toHaveValue(/Electrify America|EVgo|Tesla Supercharger/);
    const guest = card.locator("input[type=number]");
    expect(await guest.evaluateAll((els) => els.map((e) => (e as HTMLInputElement).value))).toContain("");
    // EA has both prices entered: the saving shows, and only for it.
    await expect(card.getByText(/save 0\.12\/kWh/)).toHaveCount(1);
    expect((await new AxeBuilder({ page }).include("#networks").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await ctx.close();
  });
}

test("a charge: unset networks can't be applied until a rate is set, with a way to Settings", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await setup(page);
  await page.goto("/charges/charge-1");
  // The prefill list marks the unset ones and disables them; the set one still works.
  const select = page.getByLabel("Prefill from network");
  const optDisabled = (text: string) => select.locator("option", { hasText: text }).evaluate((o) => (o as HTMLOptionElement).disabled);
  expect(await optDisabled("Tesla Supercharger - Set your rate in Settings first")).toBe(true);
  expect(await optDisabled("EVgo - Set your rate in Settings first")).toBe(true);
  expect(await optDisabled("Electrify America - 0.520 USD/kWh")).toBe(false);
  await expect(page.getByTestId("rate-unset-hint")).toContainText("Set your rate in Settings first");
  await expect(page.getByTestId("rate-unset-hint").getByRole("link", { name: "Open Settings" })).toHaveAttribute("href", "/settings?tab=charging#networks");
  // The auto-network "Use this network": disabled for an unset one.
  const panel = page.getByTestId("network-estimate");
  await panel.getByRole("button", { name: "Change" }).click();
  await panel.getByLabel("Network").selectOption("Tesla Supercharger");
  await expect(panel.getByRole("button", { name: "Use this network" })).toBeDisabled();
  await expect(panel.getByTestId("rate-unset")).toContainText("Set your rate in Settings first");
  await panel.getByLabel("Network").selectOption("Electrify America");
  await expect(panel.getByRole("button", { name: "Use this network" })).toBeEnabled();
  await expect(panel.getByTestId("rate-unset")).toHaveCount(0);
  await ctx.close();
});

// ---- Money: estimates from the tracker's typical price, never blended silently ----

const month = currentMonth(new Date(e2eNow()));

async function moneySetup(page: Page) {
  await stubApi(page);
  const base = ledgerMonthFixture(month, new Date(e2eNow())).receipts;
  const fast = base.filter((r) => r.kind === "dcfc");
  const receipts = base.map((r) => {
    if (fast[0] && r.receipt_id === fast[0].receipt_id) {
      return { ...r, cost_source: "observed_estimate" as const, estimate_month: "2026-10", network: "Electrify America", sessions: r.sessions.map((x) => ({ ...x, cost_source: "observed_estimate" as const, estimate_month: "2026-10" })) };
    }
    if (fast[1] && r.receipt_id === fast[1].receipt_id) {
      return {
        ...r, cost: null, cost_source: "unknown" as const, rate: null, needs_rate: true, rate_slug: "tesla_sc", network: "Tesla Supercharger", flags: [...r.flags, "unpriced" as const],
        sessions: r.sessions.map((x) => ({ ...x, cost: null, price_per_kwh: null, cost_source: "unknown" as const, needs_rate: true, rate_slug: "tesla_sc", network: "Tesla Supercharger" })),
      };
    }
    return r;
  });
  const m = ledgerMonthFromReceipts(month, receipts, { now: new Date(e2eNow()) });
  m.spend.observed_estimate_cost = 31.4;
  m.spend.observed_estimate_sessions = 2;
  m.spend.unpriced_fast_sessions = 1;
  await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ json: m }));
  await page.route("**/api/ledger/summary**", (r) => r.fulfill({ json: ledgerSummaryFixture(12, new Date(e2eNow())) }));
}

test("Money: estimates and unpriced fast sessions are reported on their own, with a way to set a rate", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await moneySetup(page);
  await page.goto("/money");
  const hero = page.getByRole("region", { name: "Statement" });
  await expect(hero.locator("[data-observed-estimate]")).toHaveText("Includes $31.40 estimated from typical prices (2 sessions)");
  await expect(hero.locator("[data-unpriced-fast]")).toHaveText("1 fast-charging session not priced yet");
  // The receipts say where the price came from, and each has the rate action.
  await expect(page.getByText("Estimate: typical price seen by DCFC Tracker, Oct 2026").first()).toBeVisible();
  await expect(page.getByText("Needs a price").first()).toBeVisible();
  await expect(page.locator("[data-rate-action]", { hasText: "Set your rate for Tesla Supercharger" }).first()).toHaveAttribute("href", "/settings?tab=charging#networks");
  await expect(page.locator("[data-rate-action]", { hasText: /^Set your rate →$/ }).first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await ctx.close();
});

test("Money: the notice shows once, then stays gone on this device", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await moneySetup(page);
  await page.goto("/money");
  const notice = page.locator("[data-observed-notice]");
  await expect(notice).toContainText("Fast-charging sessions without your own rate are now estimated from a typical price (DCFC Tracker), not an old table. Add the rate you pay for exact costs.");
  await expect(notice.getByRole("link", { name: "Set your rate" })).toHaveAttribute("href", "/settings?tab=charging#networks");
  expect((await new AxeBuilder({ page }).include("[data-observed-notice]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
  await notice.getByRole("button", { name: "Got it" }).click();
  await expect(notice).toHaveCount(0);
  await page.reload();
  await page.waitForTimeout(600);
  await expect(page.locator("[data-observed-notice]")).toHaveCount(0);
  await ctx.close();
});

test("Money: no estimates, no notice, no extra lines", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.goto("/money");
  await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
  await expect(page.locator("[data-observed-notice], [data-observed-estimate], [data-unpriced-fast]")).toHaveCount(0);
  await ctx.close();
});

test("a charge priced from the tracker, or waiting on a rate, says so with a way to set it", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  const charge = (n: number, o: Record<string, unknown>) => ({
    ID: `charge-${n}`, VehicleID: VEHICLE_ID, StartedAt: new Date(e2eNow() - 2 * 86_400_000).toISOString(), EndedAt: new Date(e2eNow() - 2 * 86_400_000 + 1_800_000).toISOString(),
    StartSoCPct: 20, EndSoCPct: 80, EnergyAddedKWh: 52.4, MilesAdded: 150, MaxPowerKW: 180, AvgPowerKW: 110, FinalState: "charging_complete", Lat: 40.9, Lon: -100.9, Source: "live", Currency: "USD", ...o,
  });
  await page.route("**/api/charges?**", (r) =>
    r.fulfill({
      json: [
        charge(1, { Cost: 31.4, PricePerKWh: 0.6, cost_source_effective: "observed_estimate", estimate_month: "2026-10", estimate_url: "https://dcfctracker.com/reports/electrify-america" }),
        charge(2, { Cost: 0, PricePerKWh: 0, needs_rate: true, rate_slug: "tesla_sc", network: "Tesla Supercharger", cost_source_effective: "unknown" }),
        charge(3, { Cost: 31.4, PricePerKWh: 0.6, cost_source_effective: "observed_estimate", estimate_month: "2026-10", estimate_url: "javascript:alert(1)" }),
      ],
    }),
  );
  await page.goto("/charges/charge-1");
  const note = page.locator("[data-rate-note='observed']");
  await expect(note).toContainText("Estimate: typical price seen by DCFC Tracker, Oct 2026. The price at the charger may differ.");
  await expect(note.getByRole("link", { name: "DCFC Tracker" })).toHaveAttribute("href", "https://dcfctracker.com/reports/electrify-america");
  await expect(note.getByRole("link", { name: "Set your rate" })).toHaveAttribute("href", "/settings?tab=charging#networks");
  await page.goto("/charges/charge-2");
  await expect(page.locator("[data-rate-note='needs']")).toContainText("Needs a price.");
  await expect(page.getByRole("link", { name: "Set your rate for Tesla Supercharger" })).toBeVisible();
  await page.goto("/charges/charge-3");
  await expect(page.locator("[data-rate-note='observed']").getByRole("link", { name: "DCFC Tracker" })).toHaveCount(0);
  await ctx.close();
});
