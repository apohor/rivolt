import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";

// "Free" on the Live page is Rivian's word for a charge it does not bill, believed only when it is a Rivian charger, a fast charge or AC away.
// A home AC charge whose live session is active and marked free (Rivian does not bill it) is the home rate's charge: Live and the Now card read the
// same "Cost so far".
const FULL_STATE = {
  ...JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8")),
  ...vehicleState("charging"),
};
const HOME = { elapsedMin: 86, soc: (m: number) => 40 + m * 0.2, kw: () => 7.5, targetPct: 80 };
const DC = { elapsedMin: 20, soc: (m: number) => 20 + m * 2, kw: () => 150, targetPct: 80 };
const ZERO = { at: "", vehicle_id: VEHICLE_ID, active: false, start_time: "", power_kw: 0, soc_pct: 0, total_charged_energy_kwh: 0, current_price: "", current_currency: "", range_added_km: 0, kilometers_charged_per_hour: 0, time_elapsed_seconds: 0, time_remaining_seconds: 0, is_free_session: false, is_rivian_charger: false };

async function open(page: Page, route: "/live" | "/", c: typeof HOME, session: object, homeRate = true) {
  await page.setViewportSize({ width: 393, height: 852 });
  await stubApi(page, { scenario: "charging", nowEndpoint: true, chargeCase: c, designPreview: ["now_v2", "now_tidy", "now_slot"] });
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...FULL_STATE, charger_power_kw: c.kw(0), battery_level_pct: c.soc(c.elapsedMin) }) }));
  await page.route(`**/api/live-session/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(session) }));
  if (!homeRate) {
    await page.route("**/api/settings/charging/", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ home_price_per_kwh: 0, home_currency: "USD", gas_price_per_gallon: 4, comparison_mpg: 20 }) }));
    // The server prices no charge without a home rate: the open charge has no cost.
    await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/now$`), (r) => {
      const iso = (ago: number) => new Date(Date.now() - ago).toISOString();
      return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ version: 1, at: iso(8_000), location_fix_at: iso(60_000), connection: "replay", state: "charging", state_since: iso(c.elapsedMin * 60_000), soc_at_state_start: 40, tire_pressure_min_bar: 2.8, failed_sleeps_6h: 0, charge: { started_at: iso(c.elapsedMin * 60_000), soc_start: Math.round(c.soc(0)), peak_kw: c.kw(0) }, ambient: { temp_c: 12, condition: "clear", wind_kph: 16 } }) });
    });
  }
  await page.goto(route);
}

// The cost text on a page as a user reads it (spaces dropped: the two cards lay it out differently): the cell without its label or the screen-reader prefix. Null when there is no cost cell.
async function costText(page: Page, route: "/live" | "/"): Promise<string | null> {
  const cell = route === "/live" ? page.getByTestId("live-cost-so-far") : page.getByTestId("now-slot-cost-so-far");
  if ((await cell.count()) === 0) return null;
  return (await cell.innerText()).replace(/\s+/g, "").replace(/^costsofar/i, "").replace(/^estimated/, "");
}

const CASES: { name: string; c: typeof HOME; session: object; homeRate?: boolean; want: RegExp | null }[] = [
  { name: "home AC, session active, marked free, zero price (the owner's case)", c: HOME, session: { ...ZERO, active: true, power_kw: 7.5, total_charged_energy_kwh: 10.3, time_elapsed_seconds: 5160, is_free_session: true, current_price: "0", current_currency: "USD", estimated_cost: 1.02, estimated_currency: "USD" }, want: /^\$\d+\.\d\destimate·atyourhomerate$/ },
  { name: "home AC, session active, marked free, price absent", c: HOME, session: { ...ZERO, active: true, power_kw: 7.5, is_free_session: true }, want: /^\$\d+\.\d\destimate·atyourhomerate$/ },
  { name: "home AC with no home rate", c: HOME, session: { ...ZERO, active: true, power_kw: 7.5, is_free_session: true, current_price: "0" }, homeRate: false, want: null },
  { name: "billed Rivian session", c: DC, session: { ...ZERO, active: true, power_kw: 150, total_charged_energy_kwh: 20, current_price: "9.50", current_currency: "USD", is_rivian_charger: true }, want: /^\$9\.50billedbyRivian$/ },
];

for (const k of CASES) {
  test(`@webkit live card and Now card read the same cost: ${k.name}`, async ({ page }) => {
    await open(page, "/live", k.c, k.session, k.homeRate ?? true);
    await expect(page.getByTestId("live-session-card")).toBeVisible();
    await expect(page.getByText("Free", { exact: true })).toHaveCount(0);
    const live = await costText(page, "/live");
    if (k.want) expect(live).toMatch(k.want);
    else expect(live).toBeNull();
    // No cell is labelled "Price".
    await expect(page.getByTestId("live-session-card").getByText("Price", { exact: true })).toHaveCount(0);
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await open(page, "/", k.c, k.session, k.homeRate ?? true);
    await expect(page.getByTestId("now-slot")).toBeVisible();
    if (k.want) await expect(page.getByTestId("now-slot-cost-so-far")).toBeVisible();
    expect(await costText(page, "/")).toEqual(live);
  });
}

test("@webkit live card, a truly free Rivian session: Free", async ({ page }) => {
  await open(page, "/live", DC, { ...ZERO, active: true, power_kw: 150, total_charged_energy_kwh: 20, current_price: "0", current_currency: "USD", is_free_session: true, is_rivian_charger: true });
  await expect(page.getByTestId("live-session-card").getByText("Free", { exact: true })).toBeVisible();
  await expect(page.getByTestId("live-session-card").getByText("Cost so far", { exact: true })).toBeVisible();
});

// SHOTS_DIR makes the image-gate still: the owner's case on Live at 393, dark.
test("@webkit still: live card, home AC marked free (dark, 393)", async ({ page }) => {
  test.skip(!process.env.SHOTS_DIR, "SHOTS_DIR not set");
  await page.emulateMedia({ colorScheme: "dark" });
  await open(page, "/live", HOME, { ...ZERO, active: true, power_kw: 7.5, total_charged_energy_kwh: 10.3, time_elapsed_seconds: 5160, is_free_session: true, current_price: "0", current_currency: "USD", estimated_cost: 1.02, estimated_currency: "USD" });
  await expect(page.getByTestId("live-session-card")).toBeVisible();
  await page.getByTestId("live-session-card").screenshot({ path: path.join(process.env.SHOTS_DIR as string, `${process.env.SHOT_NAME ?? "live-home-free"}.png`) });
});
