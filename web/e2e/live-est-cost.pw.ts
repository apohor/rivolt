import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";

// A home charge has no Rivian frames or price: the server sends estimated_cost from the energy recorded so far, and the
// Live card shows it as "Cost so far $X estimate, at your home rate" instead of only "waiting for first frame".
const FULL_STATE = {
  ...JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8")),
  ...vehicleState("charging"),
};
const SESSION = { active: false, power_kw: 0, total_charged_energy_kwh: 0, soc_pct: 0, current_price: "", current_currency: "", range_added_km: 0, kilometers_charged_per_hour: 0, time_elapsed_seconds: 0, time_remaining_seconds: 0 };

for (const [name, extra, expected] of [
  ["with an estimate", { estimated_cost: 3.42, estimated_currency: "USD" }, "$3.42"],
  ["without one", {}, null],
] as const) {
  test(`@webkit live card, home charge ${name}: the cost estimate shows only when the server sends one`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await stubApi(page);
    await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(FULL_STATE) }));
    await page.route(`**/api/live-session/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...SESSION, ...extra }) }));
    await page.goto("/live");
    // The truck's own power is flowing, so the line says what it shows rather than that it is waiting.
    await expect(page.getByTestId("live-status-line")).toHaveText(/^Charging · \d+(\.\d)? kW$/);
    await expect(page.getByText("waiting for first frame")).toHaveCount(0);
    const cost = page.getByTestId("live-est-cost");
    if (expected) {
      await expect(cost).toContainText("Cost so far");
      await expect(cost).toContainText(expected);
    } else {
      await expect(cost).toHaveCount(0);
    }
  });
}

for (const [name, extra, expected] of [
  ["a zero price before it is priced", { is_free_session: false }, "Estimated when it ends"],
  ["a zero price and an estimate", { estimated_cost: 3.42, estimated_currency: "USD" }, "3.42 USD estimate · home rate"],
  ["a free session", { is_free_session: true }, "Free"],
  ["a free session with an empty price and an estimate sent", { current_price: "", is_free_session: true, estimated_cost: 3.42, estimated_currency: "USD" }, "Free"],
  ["a real price", { current_price: "4.20" }, "4.20 USD"],
] as const) {
  test(`@webkit live card, fast charge with ${name}: the Price cell never reads 0 USD`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await stubApi(page);
    await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(FULL_STATE) }));
    await page.route(`**/api/live-session/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...SESSION, active: true, power_kw: 150, total_charged_energy_kwh: 20, current_price: "0", current_currency: "USD", ...extra }) }));
    await page.goto("/live");
    const cell = page.getByText("Price", { exact: true }).locator("xpath=following-sibling::div[1]");
    await expect(cell).toHaveText(expected);
  });
}
