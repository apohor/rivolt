import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// plan_v2, item 9: one arrival number on the screen (the end row says the weather-corrected figure the gauge says, Rivian's own beside it in small
// text); the folded summary never cuts the departure or the charge (places are the city, full text in the title); the phone sheet's peek does not
// repeat the totals the map pill already says.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = {
  origin: pt("Current vehicle position", 34.05, -118.24),
  destination: pt("Sacramento, California, United States", 38.58, -121.49),
  extra_stops: [],
  overnight_flags: [],
  starting_soc: "74",
  target_soc: "20",
};
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
const LEGS = [{ multiplier: 1.05, temp_c: 5, air_temp_c: 5 }, { multiplier: 1.02, temp_c: 8, air_temp_c: 8 }];
// Rivian says 24% at the destination; the weather says 18% (under the 20% target).
const CORRECTED = { ...DEMO_PLAN, weather_adjustment: { adjusted_arrival_soc: [34, 18], final_arrival_soc: 18, below_target: true, target_arrival_soc: 20, legs: LEGS } };

async function open(page: Page, plan: unknown, keys: string[], width = 1440) {
  await page.setViewportSize({ width, height: width < 500 ? 852 : 900 });
  await stubApi(page, keys.length ? { designPreview: keys } : {});
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(plan)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
}

test("weather-corrected: the end row says the gauge's figure, tinted, with Rivian's beside it", async ({ page }) => {
  await open(page, CORRECTED, ["plan_v2"]);
  const end = page.locator("[data-plan-timeline] li").last();
  await expect(end).toContainText("arrive at 18%");
  await expect(end.locator("[data-under-reserve]").first()).toBeVisible();
  await expect(end).toContainText("under your 20% target");
  await expect(end.locator("[data-rivian-arrival]")).toHaveText("· Rivian's planner: 24%");
  await expect(page.locator("[data-plan-hero] svg[role='img']")).toHaveAttribute("aria-label", /Arrival battery 18 percent, under your 20 percent target/);
  await page.screenshot({ path: path.join(SHOTS, "item9-arrival-corrected.png") });
});

// The end row wraps by piece: "arrive at 18%", "· under your 20% target" and "· Rivian's planner: 24%" each stay on one line, whatever the width.
for (const width of [1440, 393]) {
  test(`the arrival line wraps by whole pieces, never ending on a dot · ${width}`, async ({ page }) => {
    await open(page, CORRECTED, ["plan_v2"], width);
    if (width < 500) await page.locator("[data-plan-sheet-handle]").click();
    const end = page.locator("[data-plan-timeline] li").last();
    // Squeeze the row so the pieces have to wrap.
    await end.evaluate((el) => { (el as HTMLElement).style.maxWidth = "250px"; });
    const pieces = end.locator("[data-arrival-piece]");
    await expect(pieces).toHaveCount(3);
    const info = await pieces.evaluateAll((els) => els.map((e) => ({ text: (e.textContent ?? "").trim(), lines: new Set(Array.from(e.getClientRects()).map((r) => Math.round(r.top))).size })));
    expect(info.map((i) => i.lines)).toEqual([1, 1, 1]);
    expect(info.map((i) => i.text.endsWith("·"))).toEqual([false, false, false]);
    // Independent of the styling: read the row's rendered lines character by character; none may end on a dot.
    const lines = await end.evaluate((el) => {
      const byTop = new Map<number, string>();
      const w = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
      for (let n = w.nextNode(); n; n = w.nextNode()) {
        const t = n.textContent ?? "";
        for (let i = 0; i < t.length; i++) {
          const r = document.createRange();
          r.setStart(n, i); r.setEnd(n, i + 1);
          const b = r.getBoundingClientRect();
          if (!b.height) continue;
          const k = Math.round(b.top);
          byTop.set(k, (byTop.get(k) ?? "") + t[i]);
        }
      }
      return [...byTop.entries()].sort((a, b) => a[0] - b[0]).map(([, v]) => v.trim());
    });
    expect(lines.length).toBeGreaterThan(1);
    // Whole pieces wrap onto the next line; none runs past the row's edge.
    expect(await end.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    for (const l of lines) expect(l.endsWith("·")).toBe(false);
    expect(info[1].text).toBe("· under your 20% target");
    expect(info[2].text).toBe("· Rivian's planner: 24%");
    await end.evaluate((el) => el.scrollIntoView({ block: "center" }));
    await end.screenshot({ path: path.join(SHOTS, `item9-arrival-wrap-${width}.png`) });
  });
}

test("corrected to 0%: the row says 0%, amber, never Rivian's softer figure alone", async ({ page }) => {
  await open(page, { ...CORRECTED, weather_adjustment: { ...CORRECTED.weather_adjustment, adjusted_arrival_soc: [34, 0], final_arrival_soc: 0 } }, ["plan_v2"]);
  const end = page.locator("[data-plan-timeline] li").last();
  await expect(end).toContainText("arrive at 0%");
  await expect(end.locator("[data-under-reserve]").first()).toBeVisible();
  await expect(end.locator("[data-rivian-arrival]")).toHaveText("· Rivian's planner: 24%");
  await expect(page.locator("[data-plan-hero] svg[role='img']")).toHaveAttribute("aria-label", /Arrival battery 0 percent/);
});

test("no weather correction: the end row is Rivian's figure alone", async ({ page }) => {
  await open(page, DEMO_PLAN, ["plan_v2"]);
  const end = page.locator("[data-plan-timeline] li").last();
  await expect(end).toContainText("arrive at 24%");
  await expect(page.locator("[data-rivian-arrival]")).toHaveCount(0);
});

test("key off: the end row is Rivian's figure, as before", async ({ page }) => {
  await open(page, CORRECTED, []);
  const end = page.locator("[data-plan-timeline] li").last();
  await expect(end).toContainText("arrive at 24%");
  await expect(page.locator("[data-rivian-arrival]")).toHaveCount(0);
});

for (const width of [1440, 393]) {
  test(`folded summary: city names, the departure and charge whole, full text in the title · ${width}`, async ({ page }) => {
    await open(page, CORRECTED, ["plan_v2"], width);
    const edit = page.locator("[data-plan-edit]");
    await expect(edit).toContainText("Current vehicle position → Sacramento");
    await expect(edit).not.toContainText("California");
    await expect(edit).toContainText("Leaves now · 74% charge");
    await expect(edit.locator("[title]").first()).toHaveAttribute("title", /Sacramento, California, United States · Leaves now · 74% charge/);
    // Nothing is cut: the line is as tall as its text needs, so the facts' box sits inside the button.
    const inside = await edit.evaluate((b) => {
      const facts = [...b.querySelectorAll("span")].find((s) => /charge/.test(s.textContent ?? "") && s.children.length === 0)!;
      const fb = facts.getBoundingClientRect(), bb = b.getBoundingClientRect();
      return fb.right <= bb.right + 0.5 && fb.bottom <= bb.bottom + 0.5 && facts.scrollWidth <= facts.clientWidth + 1;
    });
    expect(inside).toBe(true);
    await page.screenshot({ path: path.join(SHOTS, `item9-summary-${width}.png`) });
  });
}

test("phone: the peek is the title; the totals are said once on the map pill, not again under it", async ({ page }) => {
  await open(page, CORRECTED, ["plan_v2"], 393);
  const peek = page.locator("[data-plan-sheet-title]");
  await expect(peek).toContainText("→");
  await expect(peek).not.toContainText(/charging stop/);
  await expect(page.locator("[data-plan-area='map']")).toContainText(/charging stop/);
  await page.screenshot({ path: path.join(SHOTS, "item9-peek-393.png") });
});

test("phone, key off: the peek keeps its totals line", async ({ page }) => {
  await open(page, CORRECTED, [], 393);
  await expect(page.locator("[data-plan-sheet-title]")).toContainText(/driving|charging/);
});

// v1.4.1: the weather warning says temperature and headwind in the account's units, and "feels like" only when the stop row's air temperature differs
// after rounding. temp_c is the feels-like figure, air_temp_c the plain air temperature (the server's two fields); the worst leg is the one the chip reads.
const chipFor = (temp_c: number, air_temp_c: number) => ({
  ...CORRECTED,
  weather_adjustment: { ...CORRECTED.weather_adjustment, legs: [{ multiplier: 1.05, temp_c: 8, air_temp_c: 8 }, { multiplier: 1.12, temp_c, air_temp_c, headwind_kph: 24 }] },
});
for (const u of [
  { n: "imperial", prefs: { distanceUnit: "mi", temperatureUnit: "f", temperatureUnitSet: true }, feels: "cold (feels like 41°F) + 15 mph headwind", air: "cold (46°F) + 15 mph headwind" },
  { n: "metric", prefs: { distanceUnit: "km", temperatureUnit: "c", temperatureUnitSet: true }, feels: "cold (feels like 5°C) + 24 km/h headwind", air: "cold (8°C) + 24 km/h headwind" },
]) {
  for (const same of [false, true]) {
    test(`@webkit the weather warning reads in ${u.n} units${same ? ", and says no feels like when the two temperatures are equal" : ", feels like and a headwind"}`, async ({ page }) => {
      await page.addInitScript((p) => localStorage.setItem("rivolt.preferences.v1", JSON.stringify(p)), u.prefs);
      await open(page, same ? chipFor(8, 8) : chipFor(5, 8), ["plan_v2"]);
      const chip = page.getByText("Weather pushes arrival below your target").locator("..");
      await expect(chip).toBeVisible();
      if (same) {
        await expect(chip).toContainText(`Correcting for ${u.air}`);
        await expect(chip).not.toContainText("feels like");
      } else {
        await expect(chip).toContainText(`Correcting for ${u.feels}`);
      }
      await expect(chip).toContainText("the realistic arrival is 18%");
      await chip.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `weather-chip-${u.n}-${same ? "equal" : "feels"}.png`) });
    });
  }
}
