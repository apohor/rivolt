import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, vehicleState, VEHICLE_ID } from "./stub";

// The R2 "what's been checked" card (Truck tab, dismissible once, always reachable from Settings →
// Vehicle) and field labels that follow field_support.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const R2 = {
  id: VEHICLE_ID,
  vin: "SAMPLE0000000001",
  name: "Sample R2",
  model: "R2",
  pack_kwh: 90,
  field_support: { charge_time_remaining: "estimate", power_state: "best_effort", outside_temp: "unavailable", trip_target: "unavailable" },
};

async function withR2(page: Page, vehicle: object = R2) {
  await stubApi(page);
  await page.route("**/api/vehicles", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([vehicle]) }));
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`R2 card on the Truck tab: shown once, dismissed for good, still in Settings · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await withR2(page);
    await page.goto("/truck");
    const card = page.locator("[data-r2-checked]");
    await expect(card).toContainText("Rivolt supports the R2.");
    await expect(card).toContainText("Checked on real R2s");
    await expect(card).toContainText("Shown as an estimate");
    await expect(card).toContainText("sleep and power state · reported battery capacity");
    await expect(card).not.toContainText("still being confirmed on the R2");
    await expect(card).toContainText("reported battery capacity");
    await expect(card).not.toContainText("battery health");
    await expect(card).toContainText("Not on the R2 yet");
    await expect(card).toContainText("Outside temperature doesn't come through for any truck");
    await expect(page.getByText("unverified", { exact: false })).toHaveCount(0);
    await expect(card.getByRole("link", { name: "Something looks wrong on your R2? Tell us" })).toHaveAttribute("href", /issues\/new\?template=r2-report\.yml$/);
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SHOTS, `r2-checked-${v.n}-dark.png`), fullPage: true });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await page.getByRole("button", { name: "Got it" }).click();
    await expect(card).toHaveCount(0);
    await page.reload();
    await expect(page.getByRole("heading", { name: "Truck", exact: true })).toBeVisible();
    await expect(page.locator("[data-r2-checked]")).toHaveCount(0);
    // Always reachable again from Settings → Vehicle.
    await page.goto("/settings?tab=vehicle");
    await expect(page.getByRole("heading", { name: "R2: what's been checked" })).toBeVisible();
    await expect(page.locator("[data-r2-checked]")).toContainText("Rivolt supports the R2.");
    await ctx.close();
  });
}

test("a truck that is not an R2 gets no R2 card, here or in Settings", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await withR2(page, { ...R2, name: "Sample R1S", model: "R1S", field_support: {} });
  await page.goto("/truck");
  await expect(page.getByRole("heading", { name: "Truck", exact: true })).toBeVisible();
  await expect(page.locator("[data-r2-checked]")).toHaveCount(0);
  await page.goto("/settings?tab=vehicle");
  await expect(page.getByRole("heading", { name: "Vehicle profile" })).toBeVisible();
  await expect(page.locator("[data-r2-checked]")).toHaveCount(0);
  await ctx.close();
});

// The live panel labels and hides by field_support alone: the same truck state, two support maps.
// Every VehicleState field the panel reads, so nothing renders from a missing string.
const BLANK_STATE = {
  at: "",
  vehicle_id: "",
  battery_level_pct: 0,
  distance_to_empty: 0,
  odometer_km: 0,
  gear: "",
  drive_mode: "",
  charger_state: "",
  charger_power_kw: 0,
  charge_target_pct: 0,
  charger_status: "",
  charge_port_state: "",
  remote_charging_available: "",
  latitude: 0,
  longitude: 0,
  speed_kph: 0,
  heading_deg: 0,
  altitude_m: 0,
  locked: false,
  doors_closed: false,
  frunk_closed: false,
  liftgate_closed: false,
  tailgate_closed: false,
  tonneau_closed: false,
  cabin_temp_c: 0,
  outside_temp_c: 0,
  pack_temp_avg_c: 0,
  pack_temp_max_c: 0,
  pack_temp_min_c: 0,
  cabin_preconditioning_status: "",
  power_state: "",
  alarm_sound_status: "",
  twelve_volt_battery_health: "",
  wiper_fluid_state: "",
  ota_current_version: "",
  ota_available_version: "",
  ota_status: "",
  ota_install_progress: 0,
  tire_pressure_fl_bar: 0,
  tire_pressure_fr_bar: 0,
  tire_pressure_rl_bar: 0,
  tire_pressure_rr_bar: 0,
  tire_pressure_status_fl: "",
  tire_pressure_status_fr: "",
  tire_pressure_status_rl: "",
  tire_pressure_status_rr: "",
  time_to_end_mins: 0,
  time_to_end_source: "",
  trip_target_soc_pct: 0,
  trip_target_mins_remaining: 0,
  charger_derate_status: "",
  charging_disabled_all: "",
  charging_time_estimation_validity: "",
  active_driver_name: "",
  gear_guard_locked: "",
  pet_mode_status: "",
  pet_mode_temperature_status: "",
  cabin_hold_status: "",
  window_front_left_closed: "",
  window_front_right_closed: "",
  window_rear_left_closed: "",
  window_rear_right_closed: "",
};
const LIVE_STATE = {
  ...BLANK_STATE,
  ...vehicleState("charging"),
  charging_time_estimation_validity: "valid",
  time_to_end_mins: 95,
  trip_target_soc_pct: 80,
  trip_target_mins_remaining: 30,
  outside_temp_c: 12,
  cabin_temp_c: 20,
  active_driver_name: "",
  cabin_preconditioning_status: "",
};

for (const c of [
  { name: "an R2's map", fs: R2.field_support, model: "R2", estimate: true, outside: false, target: false },
  { name: "no map (reported everywhere)", fs: {}, model: "R1S", estimate: false, outside: true, target: true },
] as const) {
  test(`live panel follows field_support: ${c.name}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await withR2(page, { ...R2, model: c.model, field_support: c.fs });
    await page.route(/\/api\/state\//, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(LIVE_STATE) }));
    await page.goto("/live");
    await expect(page.getByText("Time to full", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("Time to full (estimate)")).toHaveCount(c.estimate ? 1 : 0);
    await expect(page.getByText("Outside", { exact: true })).toHaveCount(c.outside ? 1 : 0);
    await expect(page.getByText("Trip target", { exact: true })).toHaveCount(c.target ? 1 : 0);
    await ctx.close();
  });
}
