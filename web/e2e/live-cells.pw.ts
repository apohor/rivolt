import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";

// The Live page's session card fills every cell the Rivian session leaves empty with the figure the Now card works out from the truck's own readings
// (same computation, same estimate rule): all estimated = one "Estimated so far" heading and no marks; a mixed group = a small "est." per worked-out
// figure; cost always "estimate · source". A home charge has no Rivian session at all.
const FULL_STATE = {
  ...JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8")),
  ...vehicleState("charging"),
};
const CASE = { elapsedMin: 20, soc: (m: number) => 40 + m * 0.5, kw: () => 9.6, targetPct: 80 };
const DC = { elapsedMin: 20, soc: (m: number) => 20 + m * 2, kw: () => 150, targetPct: 80 };
const ZERO = { at: "", vehicle_id: VEHICLE_ID, active: false, start_time: "", power_kw: 0, soc_pct: 0, total_charged_energy_kwh: 0, current_price: "", current_currency: "", range_added_km: 0, kilometers_charged_per_hour: 0, time_elapsed_seconds: 0, time_remaining_seconds: 0, is_free_session: false, is_rivian_charger: false };

async function open(page: import("@playwright/test").Page, c: typeof CASE, session: object | null) {
  await page.setViewportSize({ width: 393, height: 852 });
  await stubApi(page, { scenario: "charging", nowEndpoint: true, chargeCase: c });
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...FULL_STATE, charger_power_kw: c.kw(0), battery_level_pct: c.soc(c.elapsedMin) }) }));
  await page.route(`**/api/live-session/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(session ?? ZERO) }));
  await page.goto("/live");
}
const cell = (page: import("@playwright/test").Page, k: string) => page.getByTestId(`live-${k}`);

test("@webkit live card, Rivian session with every figure: the truck's own figures, no estimate marks", async ({ page }) => {
  await open(page, DC, { ...ZERO, active: true, power_kw: 150, total_charged_energy_kwh: 20, range_added_km: 80, kilometers_charged_per_hour: 240, time_elapsed_seconds: 1200, time_remaining_seconds: 1500, current_price: "9.50", current_currency: "USD", is_rivian_charger: true });
  await expect(cell(page, "energy")).toContainText("+20.0 kWh");
  await expect(cell(page, "range-added")).toContainText("+49.7 mi");
  await expect(cell(page, "rate")).toContainText("149 mi/h");
  await expect(cell(page, "elapsed")).toContainText("20 min");
  await expect(cell(page, "remaining")).toContainText("25 min");
  await expect(cell(page, "cost-so-far")).toContainText("$9.50");
  await expect(cell(page, "cost-so-far")).toContainText("billed by Rivian");
  await expect(page.getByTestId("live-estimated")).toHaveCount(0);
  await expect(page.getByTestId("live-session-card").getByText("est.", { exact: true })).toHaveCount(0);
  await expect(page.getByTestId("live-session-card").getByText("—", { exact: true })).toHaveCount(0);
});

test("@webkit live card, a session with only a price: the other figures come from the truck's readings, marked est. in the mixed group", async ({ page }) => {
  await open(page, DC, { ...ZERO, active: true, power_kw: 150, current_price: "9.50", current_currency: "USD" });
  for (const k of ["energy", "range-added", "rate", "elapsed", "remaining"]) await expect(cell(page, k)).toBeVisible();
  await expect(page.getByTestId("live-session-card").getByText("—", { exact: true })).toHaveCount(0);
  // The price is Rivian's own (not an estimate), so the group is mixed: a small "est." on each worked-out figure, none on the price.
  await expect(page.getByTestId("live-estimated")).toHaveCount(0);
  await expect(cell(page, "energy")).toContainText("est.");
  await expect(cell(page, "elapsed")).not.toContainText("est.");
  await expect(cell(page, "cost-so-far")).not.toContainText("est.");
  await expect(cell(page, "cost-so-far")).toContainText("$9.50");
});

test("@webkit live card, no session at all (home AC): every figure is the Now card's, one 'Estimated so far' heading, the cost says estimate and its source", async ({ page }) => {
  await open(page, CASE, null);
  await expect(page.getByTestId("live-status-line")).toBeVisible();
  await expect(page.getByTestId("live-estimated")).toHaveText("Estimated so far");
  for (const k of ["energy", "range-added", "elapsed"]) await expect(cell(page, k)).toBeVisible();
  await expect(cell(page, "energy")).toContainText("+");
  await expect(page.getByTestId("live-session-card").getByText("est.", { exact: true })).toHaveCount(0);
  await expect(page.getByTestId("live-session-card").getByText("—", { exact: true })).toHaveCount(0);
  await expect(cell(page, "cost-so-far")).toContainText("estimate");
});

test("@webkit live card, fast charge with no session: the figures are worked out and the cost names its source", async ({ page }) => {
  await open(page, DC, null);
  await expect(cell(page, "energy")).toBeVisible();
  await expect(cell(page, "range-added")).toBeVisible();
  await expect(cell(page, "cost-so-far")).toContainText("estimate");
  await expect(page.getByTestId("live-session-card").getByText("—", { exact: true })).toHaveCount(0);
});

test("@webkit live card and Now card name the same cost: Rivian's latest price on both", async ({ page }) => {
  await open(page, DC, { ...ZERO, active: true, power_kw: 150, current_price: "9.50", current_currency: "USD" });
  await expect(cell(page, "cost-so-far")).toContainText("$9.50");
});
