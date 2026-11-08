import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// Charge detail names the charger type with the vehicle's measured peak, and says when Money counts the session differently: anything over the AC ceiling
// is Fast here, while Money's Fast starts at 50 kW.
const charge = (o: Record<string, unknown>) => ({
  ID: "charge-ct", VehicleID: VEHICLE_ID,
  StartedAt: new Date(e2eNow() - 2 * 86_400_000).toISOString(),
  EndedAt: new Date(e2eNow() - 2 * 86_400_000 + 3_600_000).toISOString(),
  StartSoCPct: 40, EndSoCPct: 80, EnergyAddedKWh: 22, MilesAdded: 60, MaxPowerKW: 40, AvgPowerKW: 30,
  FinalState: "charging_complete", Lat: 40.9, Lon: -100.9, Source: "live", Currency: "USD", Cost: 0, PricePerKWh: 0,
  kind: "ac", place: "away", ...o,
});

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`a 40 kW session is Fast with its peak, and Money's different count is said · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/charges?**", (r) => r.fulfill({ json: [charge({})] }));
    await page.route("**/api/settings/charging/networks", (r) => r.fulfill({ json: [] }));
    await page.goto("/charges/charge-ct");
    await expect(page.getByText("Fast · peak 40 kW")).toBeVisible();
    await expect(page.locator("[data-money-bucket-note]")).toHaveText("Counted as Away in Money (Fast there means 50 kW and up).");
    // The (i) beside it opens the Fast sentence.
    await page.getByRole("button", { name: "What the charger types mean" }).first().click();
    await expect(page.getByRole("group", { name: "What the charger types mean" })).toContainText("DC fast charging, usually 50 kW and up");
    await ctx.close();
  });

  test(`a Level 2 session is Medium and has no Money note · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/charges?**", (r) => r.fulfill({ json: [charge({ MaxPowerKW: 9.6, AvgPowerKW: 9, place: "home" })] }));
    await page.route("**/api/settings/charging/networks", (r) => r.fulfill({ json: [] }));
    await page.goto("/charges/charge-ct");
    await expect(page.getByText("Medium · peak 10 kW")).toBeVisible();
    await expect(page.locator("[data-money-bucket-note]")).toHaveCount(0);
    await ctx.close();
  });
}
