import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// Design 1 (design_preview "now_v2"): the redesigned Now. Off, the screen is the one users have today.
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

async function open(page: Page, flag: boolean, scenario: "parked" | "charging" = "parked") {
  await stubApi(page, { scenario, nowEndpoint: true, designPreview: flag ? ["now_v2"] : [] });
  await page.route("**/api/settings/ambient-temp", (r) => r.fulfill(j({ enabled: true })));
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/now`), (r) => {
    const iso = (ago: number) => new Date(e2eNow() - ago).toISOString();
    return r.fulfill(
      j({
        version: 1, at: iso(8_000), location_fix_at: iso(3 * 3_600_000), connection: "replay", state: scenario, state_since: iso(2 * 3_600_000),
        soc_at_state_start: 68, tire_pressure_min_bar: 2.8, failed_sleeps_6h: 0, charge: null,
        ambient: { temp_c: 15, source: "open-meteo", as_of: iso(60_000) },
      }),
    );
  });
  await page.route(`**/api/vehicles/${VEHICLE_ID}/cold-range*`, (r) => {
    const metric = new URL(r.request().url()).searchParams.get("units") === "metric";
    return r.fulfill(
      j({
        units: metric ? "metric" : "imperial", distance_unit: metric ? "km" : "mi", efficiency_unit: metric ? "km/kWh" : "mi/kWh", temp_c: 15, efficiency_low: 2.3, efficiency_high: 2.7, range_low: metric ? 418 : 260, range_high: metric ? 438 : 272,
        based_on: { drives: 9, miles: 240, temp_lo_c: 11, temp_hi_c: 19 }, available: [15], min_drives: 5, min_miles: 50,
      }),
    );
  });
  await page.goto("/");
}

test("@webkit flag on: the new hero shows the chip, the full-charge estimate and the month strip", async ({ page }) => {
  await open(page, true);
  const hero = page.locator("[data-now-v2]");
  await expect(hero).toBeVisible();
  await expect(page.locator("[data-now-chip]")).toBeVisible();
  await expect(page.getByTestId("now-subline")).toHaveText(/Full charge today ≈ 266 mi/);
  await expect(page.getByTestId("now-temps")).toContainText("Outside");
  // State and time in the chip; no metadata line; the card opens Full telemetry; "estimate" sits behind the (i).
  await expect(page.locator("[data-now-chip]")).toHaveText(/(Parked|Plugged in|Asleep) · since \d/);
  await expect(page.getByTestId("now-freshness")).toHaveCount(0);
  await expect(hero.getByRole("link", { name: "Full telemetry" })).toHaveAttribute("href", "/live");
  await expect(page.getByTestId("now-temps")).not.toContainText("estimate");
  await page.getByRole("button", { name: "About the outside temperature" }).click();
  await expect(page.getByTestId("now-temps")).toContainText("estimate");
  await expect(page.getByTestId("now-month")).toBeVisible();
  await expect(page.getByTestId("now-month")).toContainText("miles");
  await expect(page.getByTestId("now-month")).toContainText("kWh added");
  await expect(page.getByTestId("now-month")).toContainText("mi/kWh");
  await expect(page.getByTestId("now-month")).toContainText("spent");
  await expect(page.getByText("Since last charge")).toHaveCount(0);
  await expect(page.locator("[data-weather-credit]")).toBeVisible();
});

test("flag on: the (i) has a 44 px target, a tap beside it stays on the page, and the credit sits under the strip", async ({ page }) => {
  await open(page, true);
  const btn = page.getByRole("button", { name: "About the outside temperature" });
  const box = (await btn.boundingBox())!;
  expect(box.width).toBeGreaterThanOrEqual(44);
  expect(box.height).toBeGreaterThanOrEqual(44);
  // 18 px icon, so 18 px off its centre is a near miss that must still hit the button, not the card link.
  const url = page.url();
  await page.mouse.click(box.x + box.width / 2 + 18, box.y + box.height / 2);
  await expect(page.getByTestId("now-temps")).toContainText("estimate");
  expect(page.url()).toBe(url);
  const strip = (await page.getByTestId("now-month").boundingBox())!;
  const credit = (await page.locator("[data-weather-credit]").boundingBox())!;
  expect(credit.y).toBeGreaterThan(strip.y + strip.height - 1);
});

test("@webkit flag on: no month-strip cell clips at 393 px", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await open(page, true);
  const strip = page.getByTestId("now-month");
  await expect(strip).toBeVisible();
  // The widest plausible figure, so the check does not depend on the stub's totals.
  await strip.evaluate((el) => {
    const nums = el.querySelectorAll("div > div > span:first-child");
    nums[nums.length - 1].textContent = "$1,135.99";
  });
  const clipped = await strip.evaluate((el) => {
    const box = el.getBoundingClientRect();
    return [...el.querySelectorAll("div > div > span")].filter((s) => {
      const r = s.getBoundingClientRect();
      return r.right > box.right + 0.5 || s.scrollWidth > s.clientWidth + 0.5;
    }).length;
  });
  expect(clipped).toBe(0);
});

test("flag on, charging: the chip says power and the subline says what the limit buys", async ({ page }) => {
  await open(page, true, "charging");
  await expect(page.locator("[data-now-chip]")).toContainText(/Charging · .* kW · (done|full) by \d/);
  await expect(page.getByTestId("now-subline")).toContainText(/Charging to \d+%/);
});

test("flag on: stepping the period (picker above) renames the strip", async ({ page }) => {
  await open(page, true);
  await expect(page.getByTestId("now-month").getByRole("heading")).toHaveText("This month");
  // The picker is in the strip, not the page header.
  await expect(page.locator('[data-ui-area="scope"]').getByRole("button", { name: "Previous period" })).toHaveCount(0);
  await page.getByTestId("now-month").getByRole("button", { name: "Previous period" }).click();
  await expect(page.getByTestId("now-month").getByRole("heading")).not.toHaveText("This month");
  await expect(page.getByRole("region", { name: await page.getByTestId("now-month").getByRole("heading").innerText() })).toBeVisible();
});

test("flag off: the screen is today's (no new hero, the KPI tiles are there)", async ({ page }) => {
  await open(page, false);
  await expect(page.getByTestId("now-headline")).toBeVisible();
  await expect(page.locator("[data-now-v2]")).toHaveCount(0);
  await expect(page.getByTestId("now-month")).toHaveCount(0);
  await expect(page.getByText("Since last charge")).toBeVisible();
});

test("flag missing from /api/config: also today's screen", async ({ page }) => {
  await stubApi(page, { scenario: "parked", designPreview: null });
  await page.goto("/");
  await expect(page.getByTestId("now-headline")).toBeVisible();
  await expect(page.locator("[data-now-v2]")).toHaveCount(0);
});

test("flag on: every distance on the hero is in the user's unit (km)", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: "km" })));
  await open(page, true);
  const hero = page.locator("[data-now-v2]");
  await expect(hero).toContainText("km");
  await expect(hero).not.toContainText(/\d mi\b/);
  await expect(page.getByTestId("now-month")).toContainText("km/kWh");
  await expect(page.getByTestId("now-month")).not.toContainText("mi/kWh");
});
