import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, vehicleState, VEHICLE_ID } from "./stub";

// v0.33.2: Activity never says "Charging now" on ended sessions; Truck has
// no empty card, no "Update available 0.0.0", and a sleep card that
// doesn't claim "no data" while loading.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ch = (n: number, daysAgo: number, o: Record<string, unknown>) => ({
  ID: `charge-${n}`, VehicleID: VEHICLE_ID,
  StartedAt: new Date(Date.now() - daysAgo * 86_400_000 - 3_600_000).toISOString(),
  EndedAt: new Date(Date.now() - daysAgo * 86_400_000).toISOString(),
  StartSoCPct: 30, EndSoCPct: 80, EnergyAddedKWh: 40, MilesAdded: 120, MaxPowerKW: 150, AvgPowerKW: 100,
  Lat: 40.9, Lon: -100.9, Source: "live", Currency: "USD", Cost: 18, PricePerKWh: 0.4, CostSource: "network_estimate", ...o,
});

for (const [vpName, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`activity + truck · ${vpName} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await stubApi(page);
      await page.route("**/api/charges?**", (r) =>
        r.fulfill({
          json: [
            ch(1, 3, { FinalState: "charging_user_stopped" }),
            ch(2, 4, { FinalState: "charging_station_stopped" }),
            ch(3, 5, { FinalState: "charging_active" }), // stuck open, silent for days
            ch(4, 6, { FinalState: "charging_complete" }),
          ],
        }),
      );
      await page.route(`**/api/state/${VEHICLE_ID}`, (r) =>
        r.fulfill({ json: { ...vehicleState("parked"), ota_current_version: "2026.36.0", ota_available_version: "0.0.0" } }),
      );
      await page.goto("/activity");
      await expect(page.getByText(/\$18/).first()).toBeVisible();
      await expect(page.getByText("Charging now")).toHaveCount(0);
      await page.screenshot({ path: path.join(SHOTS, `defects-activity-${vpName}-${theme}.png`) });

      await page.goto("/truck");
      await expect(page.getByText("Software")).toBeVisible();
      await expect(page.getByText("Update available")).toHaveCount(0);
      await expect(page.getByText("0.0.0")).toHaveCount(0);
      await page.waitForTimeout(500);
      // No bordered placeholder with nothing in it.
      const empties = await page.evaluate(() =>
        [...document.querySelectorAll("main section, main div")].filter(
          (e) => e.className.toString().includes("rounded-xl") && e.className.toString().includes("border") && (e.textContent ?? "").trim() === "" && e.querySelector("svg,canvas,img") == null,
        ).length,
      );
      expect(empties).toBe(0);
      await page.screenshot({ path: path.join(SHOTS, `defects-truck-${vpName}-${theme}.png`), fullPage: true });
      await ctx.close();
    });
  }
}
