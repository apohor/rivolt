import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// Pricing a charge the server still counts as open answers 409 with the plain text "charge still in progress". The form says so in words, not "Error: ..." and not
// "try again"; and an open charge's Cost card doesn't point at that form.
const charge = (o: Record<string, unknown>) => ({
  ID: "charge-pe", VehicleID: VEHICLE_ID,
  StartedAt: new Date(e2eNow() - 2 * 3_600_000).toISOString(),
  EndedAt: new Date(e2eNow() - 2 * 86_400_000 + 3_600_000).toISOString(),
  StartSoCPct: 40, EndSoCPct: 80, EnergyAddedKWh: 52, MilesAdded: 120, MaxPowerKW: 11, AvgPowerKW: 9.6,
  FinalState: "charging_complete", Lat: 40.9, Lon: -100.9, Source: "live", Currency: "USD", Cost: 0, PricePerKWh: 0,
  kind: "ac", place: "away", cost_source_effective: "unknown", ...o,
});

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`pricing a still-open charge says so in the form · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/charges?**", (r) => r.fulfill({ json: [charge({})] }));
    await page.route("**/api/settings/charging/networks", (r) => r.fulfill({ json: [] }));
    await page.route("**/api/charges/charge-pe/pricing", (r) => r.fulfill({ status: 409, contentType: "text/plain; charset=utf-8", body: "charge still in progress\n" }));
    await page.goto("/charges/charge-pe");
    await page.getByRole("button", { name: "Mark as free" }).click();
    const alert = page.getByRole("alert").filter({ hasText: "still open" });
    await expect(alert).toHaveText("This charge is still open, so it can't be priced yet. Rivolt can price it once it ends.");
    await expect(page.getByText("Error:")).toHaveCount(0);
    await ctx.close();
  });

  test(`an open charge's Cost card does not point at the pricing form · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/charges?**", (r) => r.fulfill({ json: [charge({ FinalState: "charging_active", EndedAt: new Date(e2eNow() - 60_000).toISOString() })] }));
    await page.route("**/api/settings/charging/networks", (r) => r.fulfill({ json: [] }));
    await page.goto("/charges/charge-pe");
    await expect(page.getByText("price it once the charge ends", { exact: true })).toBeVisible();
    await expect(page.getByText("add it below")).toHaveCount(0);
    await ctx.close();
  });
}
