import { expect, test, type Page } from "@playwright/test";
import { stubApi, vehicleState, VEHICLE_ID } from "./stub";

// Every VehicleState field the Live panel reads, so nothing renders from a missing string.
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

// The drive and charge lists are big. They load once and stay cached across screens, and the Live panel does not
// refetch them while a session is under way.

async function counted(page: Page, scenario: "parked" | "charging" = "parked") {
  await stubApi(page, { scenario });
  const calls: { path: string; limit: string }[] = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (r.method() === "GET" && (u.pathname === "/api/drives" || u.pathname === "/api/charges")) calls.push({ path: u.pathname, limit: u.searchParams.get("limit") ?? "" });
  });
  return calls;
}
const big = (calls: { path: string; limit: string }[], path: string) => calls.filter((c) => c.path === path && c.limit === "5000").length;

test("the full lists load once and are reused across screens", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const calls = await counted(page);
  await page.goto("/");
  await expect.poll(() => big(calls, "/api/drives")).toBe(1);
  // Activity, back to Now, then Money: the same cached lists, no second fetch.
  await page.getByRole("link", { name: "Activity" }).click();
  await expect(page).toHaveURL(/\/activity/);
  await page.getByRole("link", { name: "Now" }).click();
  await expect(page).toHaveURL(/\/$/);
  await page.waitForTimeout(800);
  expect(big(calls, "/api/drives")).toBe(1);
  expect(big(calls, "/api/charges")).toBe(1);
  await ctx.close();
});

test("the Live view asks for one drive and one charge, and does not poll the lists while a session is in flight", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const calls = await counted(page);
  // Moving (so a session counts as in flight, the case that used to poll every 5 s) while the panel shows the parked summary.
  await page.route(new RegExp(`/api/state/${VEHICLE_ID}`), (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...BLANK_STATE, ...vehicleState("parked"), speed_kph: 5 }) }),
  );
  await page.goto("/live");
  await expect.poll(() => calls.filter((c) => c.limit === "1").length).toBeGreaterThanOrEqual(2);
  const before = calls.length;
  // Longer than the old 5 s list poll.
  await page.waitForTimeout(12_000);
  expect(big(calls, "/api/drives")).toBe(0);
  expect(big(calls, "/api/charges")).toBe(0);
  expect(calls.length).toBe(before);
  await ctx.close();
});
