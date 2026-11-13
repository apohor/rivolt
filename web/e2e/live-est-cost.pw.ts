import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";

// A home charge has no Rivian frames or price: the server sends estimated_cost from the energy recorded so far, and the
// Live card shows it as "Cost so far ~$X at your home rate" instead of only "waiting for first frame".
const FULL_STATE = {
  ...JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8")),
  ...vehicleState("charging"),
};
const SESSION = { active: false, power_kw: 0, total_charged_energy_kwh: 0, soc_pct: 0, current_price: "", current_currency: "", range_added_km: 0, kilometers_charged_per_hour: 0, time_elapsed_seconds: 0, time_remaining_seconds: 0 };

for (const [name, extra, expected] of [
  ["with an estimate", { estimated_cost: 3.42, estimated_currency: "USD" }, "~$3.42"],
  ["without one", {}, null],
] as const) {
  test(`@webkit live card, home charge ${name}: the cost estimate shows only when the server sends one`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await stubApi(page);
    await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(FULL_STATE) }));
    await page.route(`**/api/live-session/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...SESSION, ...extra }) }));
    await page.goto("/live");
    await expect(page.getByText("waiting for first frame")).toBeVisible();
    const cost = page.getByTestId("live-est-cost");
    if (expected) {
      await expect(cost).toContainText("Cost so far");
      await expect(cost).toContainText(expected);
    } else {
      await expect(cost).toHaveCount(0);
    }
  });
}
