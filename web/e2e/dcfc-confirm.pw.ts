import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { currentMonth, ledgerMonthFixture } from "../src/lib/ledger";

// qa2: after Change/Confirm the charge page's manual price field must
// follow the server, and a confirmed Money row must stop reading as an
// estimate.

const charge = (o: Record<string, unknown>) => ({
  ID: "charge-2", VehicleID: VEHICLE_ID,
  StartedAt: new Date(Date.now() - 2 * 86_400_000).toISOString(),
  EndedAt: new Date(Date.now() - 2 * 86_400_000 + 1_800_000).toISOString(),
  StartSoCPct: 20, EndSoCPct: 80, EnergyAddedKWh: 86.6, MilesAdded: 150, MaxPowerKW: 180, AvgPowerKW: 110,
  FinalState: "charging_complete", Lat: 40.9, Lon: -100.9, Source: "live", Currency: "USD", ...o,
});

test("Change network: the manual price field follows the new price without a reload", async ({ page }) => {
  await stubApi(page);
  let current = charge({
    Cost: 34.65, PricePerKWh: 0.4, CostSource: "network_estimate", network_state: "needs_confirmation", network_confidence: 0.6,
    network_evidence: { v: 1, source: "archive", candidates: [{ network: "Tesla Supercharger", slug: "tesla_sc", dist_m: 38 }], rate_basis: "member" },
  });
  await page.route("**/api/charges?**", (r) => r.fulfill({ json: [current] }));
  await page.route("**/api/settings/charging/networks", (r) =>
    r.fulfill({
      json: [
        { name: "Tesla Supercharger", price_per_kwh: 0.5, member_price_per_kwh: 0.4, member_active: true, currency: "USD", slug: "tesla_sc" },
        { name: "Electrify America", price_per_kwh: 0.48, currency: "USD", slug: "ea" },
      ],
    }),
  );
  await page.route("**/api/charges/charge-2/pricing", (r) => {
    current = charge({ Cost: 41.57, PricePerKWh: 0.48, CostSource: "user", network_state: "confirmed" });
    return r.fulfill({ status: 204, body: "" });
  });
  await page.goto("/charges/charge-2");
  const ppk = page.getByLabel("Price per kWh");
  await expect(ppk).toHaveValue("0.400");
  await page.getByTestId("network-estimate").getByRole("button", { name: "Change" }).click();
  await page.getByTestId("network-estimate").getByLabel("Network").selectOption("Electrify America");
  await page.getByRole("button", { name: "Use this network" }).click();
  await expect(ppk).toHaveValue("0.480");
  await expect(page.getByText(/≈ \$41\.57/).first()).toBeVisible();
  await expect(page.getByTestId("network-confirmed")).toBeVisible();
});

test("a confirmed network reads as a recorded price on Money, not an estimate", async ({ page }) => {
  await stubApi(page);
  await page.route("**/api/ledger/monthly**", (r) => {
    const m = ledgerMonthFixture(currentMonth());
    for (const rc of m.receipts) {
      if (rc.cost_source === "network_estimate") {
        rc.network_state = "confirmed";
        rc.sessions.forEach((s) => (s.network_state = "confirmed"));
      }
    }
    return r.fulfill({ json: m });
  });
  await page.goto("/money");
  await expect(page.getByText(/^Confirmed · Electrify America/).first()).toBeVisible();
  // The fixture's only network_estimate receipt is confirmed: no est. tag left.
  await expect(page.getByText("est.", { exact: true })).toHaveCount(0);
});
