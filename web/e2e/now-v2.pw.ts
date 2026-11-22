import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// The redesigned Now.
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

async function open(page: Page, scenario: "parked" | "asleep" | "charging" | "driving" = "parked", ambientOn = true) {
  await stubApi(page, { scenario, nowEndpoint: true });
  await page.route("**/api/settings/ambient-temp", (r) => r.fulfill(j({ enabled: ambientOn })));
  // The "Three things" card has something to offer.
  await page.route("**/api/push/status", (r) => r.fulfill(j({ enabled: true, subscription_count: 0 })));
  await page.route("**/api/settings/statement-email", (r) => r.fulfill(j({ enabled: false, available: true })));
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/now`), (r) => {
    const iso = (ago: number) => new Date(e2eNow() - ago).toISOString();
    return r.fulfill(
      j({
        version: 1, at: iso(8_000), location_fix_at: iso(3 * 3_600_000), connection: "replay", state: scenario, state_since: iso(2 * 3_600_000),
        soc_at_state_start: 68, tire_pressure_min_bar: 2.8, failed_sleeps_6h: 0, charge: null,
        ...(scenario === "asleep" ? { asleep_since: iso(2 * 3_600_000), asleep_today_s: 18 * 3600 + 40 * 60 } : {}),
        ambient: { temp_c: 15, source: "open-meteo", as_of: iso(60_000), condition: "partly_cloudy", wind_kph: 16 },
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
  await open(page);
  const hero = page.locator("[data-now-v2]");
  await expect(hero).toBeVisible();
  await expect(page.locator("[data-now-chip]")).toBeVisible();
  await expect(page.getByTestId("now-subline")).toHaveText(/Full charge today 266 mi/);
  await expect(page.getByTestId("now-temps")).toContainText("Outside");
  // State and time in the chip; no metadata line; only the Live button opens Live; "estimate" sits behind the (i).
  await expect(page.locator("[data-now-chip]")).toHaveText(/(Parked|Plugged in|Asleep) · since \d/);
  await expect(page.getByTestId("now-freshness")).toHaveCount(0);
  await expect(hero.getByTestId("now-live-link")).toHaveAttribute("href", "/live");
  await expect(hero.getByRole("link", { name: "Full telemetry" })).toHaveCount(0);
  await expect(hero.getByTestId("now-live-link")).toHaveText(/^Live/);
  // The only "Live" on the card is the button; the chip names the state.
  await expect(page.locator("[data-now-chip]")).not.toHaveText(/\bLive\b/);
  await expect(hero.getByTestId("now-live-link")).toHaveAttribute("href", "/live");
  await expect(page.getByTestId("now-month")).toBeVisible();
  await expect(page.getByTestId("now-month")).toContainText("miles");
  await expect(page.getByTestId("now-month")).toContainText("kWh added");
  await expect(page.getByTestId("now-month")).toContainText("mi/kWh");
  await expect(page.getByTestId("now-month")).toContainText("spent");
  await expect(page.getByText("Since last charge")).toHaveCount(0);
  // The Open-Meteo credit is in the page footer, next to Credits, not in the Now body.
  await expect(page.locator("footer [data-weather-credit]")).toBeVisible();
  await expect(page.locator("main [data-weather-credit]")).toHaveCount(0);
});

test.describe("touch", () => {
  test.use({ hasTouch: true, isMobile: true });
  test("flag on: the Live pill has a 44 px target, no pulse while parked, and opens /live", async ({ page }) => {
    await live44(page);
  });
});

async function live44(page: Page) {
  await open(page);
  const live = page.getByTestId("now-live-link");
  const box = (await live.boundingBox())!;
  expect(box.width).toBeGreaterThanOrEqual(44);
  expect(box.height).toBeGreaterThanOrEqual(44);
  await expect(live.locator(".animate-pulse")).toHaveCount(0);
  await live.click();
  await expect(page).toHaveURL(/\/live$/);
}

test("flag on, charging: the Live pill pulses", async ({ page }) => {
  await open(page, "charging");
  await expect(page.getByTestId("now-live-link").locator(".animate-pulse")).toHaveCount(1);
});

for (const width of [1440, 1280, 393]) {
test(`flag on: the Three things card is last on Now, below the Battery trend and above the footer (${width} px)`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 });
  await open(page);
  const card = page.locator("[data-three-things]");
  await expect(card).toBeVisible();
  const trend = (await page.getByRole("heading", { name: "Battery (SoC) trend" }).boundingBox())!;
  const c = (await card.boundingBox())!;
  const foot = (await page.locator("footer").boundingBox())!;
  expect(c.y).toBeGreaterThan(trend.y);
  expect(c.y + c.height).toBeLessThanOrEqual(foot.y + 1);
});
}

test("flag on: the (i) has a 44 px target and a tap beside it stays on the page", async ({ page }) => {
  await open(page);
  const btn = page.getByRole("button", { name: "About the outside temperature" });
  const box = (await btn.boundingBox())!;
  expect(box.width).toBeGreaterThanOrEqual(44);
  expect(box.height).toBeGreaterThanOrEqual(44);
  // 18 px icon, so 18 px off its centre is a near miss that must still hit the button, not Live.
  const url = page.url();
  await page.mouse.click(box.x + box.width / 2 + 18, box.y + box.height / 2);
  await expect(page.locator("[data-now-tidy]")).toContainText("looked up from Open-Meteo");
  expect(page.url()).toBe(url);
});

for (const width of [393, 440, 519, 520, 639, 640, 700, 768, 900, 1024, 1280]) test(`@webkit flag on: no month-strip cell clips at ${width} px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 852 });
  await open(page);
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
  await open(page, "charging");
  await expect(page.locator("[data-now-chip]")).toContainText(/Charging · .* kW · (done|full) by \d/);
  await expect(page.getByTestId("now-subline")).toContainText(/Charging to \d+%/);
});

test("flag on: stepping the period (picker above) renames the strip", async ({ page }) => {
  await open(page);
  // In the first hours of a month (in UTC) the month holds nothing yet and the strip shows the last 30 days (earlyMonthFallback), by design.
  const heading = page.getByTestId("now-month").getByRole("heading").first();
  await expect(heading).toHaveText(/^(This month|[A-Z][a-z]{2} \d+ – [A-Z][a-z]{2} \d+, \d{4})$/);
  const before = await heading.innerText();
  // The picker is in the strip, not the page header.
  await expect(page.locator('[data-ui-area="scope"]').getByRole("button", { name: "Previous period" })).toHaveCount(0);
  await page.getByTestId("now-month").getByRole("button", { name: "Previous period" }).click();
  await expect(heading).not.toHaveText(before);
  await expect(page.getByRole("region", { name: await heading.innerText() })).toBeVisible();
});

test("flag on: every distance on the hero is in the user's unit (km)", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: "km" })));
  await open(page);
  const hero = page.locator("[data-now-v2]");
  await expect(hero).toContainText("km");
  await expect(hero).not.toContainText(/\d mi\b/);
  await expect(page.getByTestId("now-month")).toContainText("km/kWh");
  await expect(page.getByTestId("now-month")).not.toContainText("mi/kWh");
});

test("design now_slot: charging figures replace the picture; without the key the picture stays", async ({ page }) => {
  await open(page, "charging");
  const slot = page.getByTestId("now-slot");
  await expect(slot).toBeVisible();
  await expect(slot.getByTestId("now-slot-big")).toContainText("kW");
  await expect(page.getByTestId("now-car")).toHaveCount(0);
});

test("design now_slot: parked shows the map and the weather in place of the picture", async ({ page }) => {
  await open(page, "parked");
  const slot = page.getByTestId("now-slot");
  await expect(slot).toBeVisible();
  await expect(slot.getByTestId("now-slot-map")).toBeVisible();
  await expect(slot.getByTestId("now-slot-weather")).toHaveCount(0);
  await expect(page.getByTestId("now-temps")).toContainText("Partly cloudy");
  await expect(page.getByTestId("now-temps")).not.toContainText("next 3 h");
  await expect(page.getByTestId("now-car")).toHaveCount(0);
  // The full-charge line stays, and the outside temperature is shown once (in the weather), not twice.
  await expect(page.getByTestId("now-subline")).toContainText("Full charge today");
  // Not interactive: no zoom buttons.
  await expect(slot.locator(".maplibregl-ctrl-zoom-in")).toBeHidden();
});

test("design now_slot: driving figures come from /now, with no live-drive record", async ({ page }) => {
  await open(page, "driving");
  await page.route(`**/api/live-drive/${VEHICLE_ID}`, (r) => r.fulfill({ status: 204 }));
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/now`), (r) => {
    const iso = (ago: number) => new Date(e2eNow() - ago).toISOString();
    return r.fulfill(
      j({
        version: 1, at: iso(8_000), location_fix_at: iso(8_000), connection: "replay", state: "driving", state_since: iso(600_000),
        soc_at_state_start: 68, tire_pressure_min_bar: 2.8, failed_sleeps_6h: 0, charge: null,
        drive: { started_at: iso(600_000), distance_mi: 10.4, duration_s: 600, energy_kwh: 2.7, efficiency_mi_per_kwh: 10.4 / 2.7, avg_speed_mph: 62 },
      }),
    );
  });
  const slot = page.getByTestId("now-slot");
  await expect(slot.getByTestId("now-slot-distance")).toContainText("10.4 mi");
  await expect(slot.getByTestId("now-slot-time")).toContainText("10 min");
  await expect(slot.getByTestId("now-slot-efficiency")).toContainText("3.9 mi/kWh");
  await expect(slot.getByTestId("now-slot-energy-used")).toContainText("2.7 kWh");
});

// Design "now_tidy": one hierarchy in the hero (status row, battery and range, the state's slot, one row of conditions); the month's blocks
// under one heading; the setup cards last.
test.describe("design now_tidy, phone and desktop", () => {
  for (const width of [393, 1280]) {
    test(`asleep, ${width} px: the status is said once, the conditions share one row of equal items, setup cards come last`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 });
      await open(page, "asleep");
      const hero = page.locator("[data-now-tidy]");
      await expect(hero).toBeVisible();
      // The chip sits in the card, on the row of the Live button, and not in the page header.
      const chip = (await hero.locator("[data-now-chip]").boundingBox())!;
      const live = (await hero.getByTestId("now-live-link").boundingBox())!;
      expect(Math.abs(chip.y + chip.height / 2 - (live.y + live.height / 2))).toBeLessThan(6);
      await expect(page.locator("[data-now-chip-slot]")).toHaveCount(0);
      await expect(hero.getByTestId("now-slot-map")).toBeVisible();
      // Outside, wind, rain and cabin: one label row, one value row (equal tops per row).
      const items = hero.getByTestId("now-temps").locator("> div");
      expect(await items.count()).toBeGreaterThanOrEqual(3);
      const heights = await items.evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().height)));
      expect(new Set(heights).size).toBe(1);
      await expect(hero.getByTestId("now-temps")).toContainText("Wind");
      await expect(hero.getByTestId("now-temps")).toContainText("Cabin");
      await expect(hero.getByTestId("now-slot-weather")).toHaveCount(0);
      // An odd count ends in a footer line (label left, value right under a hairline), not a lone cell in the grid.
      const foot = hero.getByTestId("now-cond-asleep");
      const g = (await hero.getByTestId("now-temps").boundingBox())!;
      const f = (await foot.boundingBox())!;
      expect(f.y).toBeGreaterThan(g.y + g.height);
      expect(Math.abs(f.width - g.width)).toBeLessThan(2);
      // Month blocks are inside the one section; the setup cards follow the charts and close the page.
      const month = page.getByTestId("now-month");
      await expect(month.getByRole("heading", { name: "Miles per day" })).toBeVisible();
      await expect(month.getByRole("heading", { name: "Battery (SoC) trend" })).toBeVisible();
      const trend = (await page.getByRole("heading", { name: "Battery (SoC) trend" }).boundingBox())!;
      const things = (await page.locator("[data-three-things]").boundingBox())!;
      expect(things.y).toBeGreaterThan(trend.y);
      const first = (await page.getByText("Your first charge").boundingBox())!;
      expect(first.y).toBeGreaterThan(trend.y);
      expect(things.y).toBeGreaterThan(first.y);
    });
  }

  test("320 px: the status chip and the Live button share one line", async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 900 });
    await open(page, "asleep");
    const chip = (await page.locator("[data-now-tidy] [data-now-chip]").boundingBox())!;
    const live = (await page.getByTestId("now-live-link").boundingBox())!;
    expect(chip.height).toBeLessThan(34);
    expect(chip.x + chip.width).toBeLessThanOrEqual(live.x);
  });

  for (const width of [320, 360, 390]) {
    test(`${width} px, charging with no slot: a long chip is cut short, Live stays inside the card, no sideways scroll`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await open(page, "charging");
      const card = (await page.locator("[data-now-tidy]").boundingBox())!;
      const chip = (await page.locator("[data-now-tidy] [data-now-chip]").boundingBox())!;
      const live = (await page.getByTestId("now-live-link").boundingBox())!;
      expect(chip.height).toBeLessThan(34);
      expect(live.x + live.width).toBeLessThanOrEqual(card.x + card.width);
      expect(chip.x + chip.width).toBeLessThanOrEqual(live.x);
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
    });
  }

  test("charging: the chip names the state and the slot keeps its figures", async ({ page }) => {
    await open(page, "charging");
    await expect(page.locator("[data-now-chip]").first()).toHaveText(/^Charging/);
    await expect(page.getByTestId("now-slot-big")).toContainText("kW");
    await expect(page.getByTestId("now-slot-big")).toContainText("Home charger");
    await expect(page.getByTestId("now-live-link").locator(".animate-pulse")).toHaveCount(1);
  });

  test("driving: speed and drive figures stay", async ({ page }) => {
    await open(page, "driving");
    await expect(page.locator("[data-now-chip]")).toHaveText("Driving");
    await expect(page.getByTestId("now-slot-big")).toContainText("mph");
  });

  test("the (i) beside Outside explains the estimate", async ({ page }) => {
    await open(page, "parked");
    await page.getByRole("button", { name: "About the outside temperature" }).click();
    await expect(page.locator("[data-now-tidy]")).toContainText("looked up from Open-Meteo");
  });

});

// Only the Live button opens Live: a tap on the ring, the figures, the graph, the weather row or the location row does nothing.
for (const scenario of ["parked", "asleep", "charging", "driving"] as const) {
  test(`the card is not a link, only Live is: ${scenario}`, async ({ page }) => {
    await open(page, scenario);
    const hero = page.locator("[data-now-v2]").first();
    await expect(hero).toBeVisible();
    await expect(hero.getByRole("link")).toHaveCount(1);
    await expect(hero.getByRole("link", { name: /telemetry/i })).toHaveCount(0);
    const url = page.url();
    const box = (await hero.boundingBox())!;
    for (const [fx, fy] of [[0.5, 0.35], [0.3, 0.55], [0.7, 0.8], [0.5, 0.95]]) {
      await page.mouse.click(box.x + box.width * fx, box.y + box.height * fy);
      expect(page.url()).toBe(url);
    }
    for (const id of ["now-parked-graph", "now-charge-graph", "now-parked-place", "now-temps", "now-slot"]) {
      const el = hero.getByTestId(id).first();
      if (await el.count()) {
        await el.click({ position: { x: 4, y: 4 }, force: true });
        expect(page.url()).toBe(url);
        expect(await el.evaluate((e) => getComputedStyle(e).cursor)).not.toBe("pointer");
      }
    }
    expect(await hero.evaluate((e) => getComputedStyle(e).cursor)).not.toBe("pointer");
    await hero.getByTestId("now-live-link").click();
    await expect(page).toHaveURL(/\/live$/);
  });
}
