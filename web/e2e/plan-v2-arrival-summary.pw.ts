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
