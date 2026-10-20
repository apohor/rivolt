import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

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
