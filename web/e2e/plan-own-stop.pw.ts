import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Your own overnight stop: any place, with the hookup you choose. The planned power follows the hookup (7 kW by default,
// editable from 1.0 to 11.5), the note names Rivian's Portable Charger, and the night is planned and saved with its hookup.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Pasadena", 34.15, -118.14), destination: pt("Mammoth Lakes", 37.65, -118.97), extra_stops: [], overnight_flags: [] };

async function open(page: import("@playwright/test").Page, more?: () => Promise<void>) {
  await stubApi(page);
  // Routes go after the stub (its catch-all would override earlier ones).
  await more?.();
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: "st-one", name: "Pasadena to Mammoth", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]) }),
  );
  await page.route("**/api/geocode*", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ name: "Mono Lake Campground", latitude: 37.99, longitude: -119.11, country: "US", admin1: "California" }]) }),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Pasadena to Mammoth/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  const bar = page.locator("[data-plan-bar]");
  await bar.getByRole("button", { name: "+ Add an overnight" }).click();
  await bar.getByPlaceholder("Search city or place…").fill("Mono Lake");
  await page.getByRole("button", { name: /Mono Lake Campground/ }).first().click();
  return bar;
}

for (const w of [390, 1280] as const) {
  test(`pick a hookup for your own overnight, see the note and the planned power · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page);
    const night = page.locator("[data-overnight-stop]").first();
    await expect(night).toContainText("Mono Lake Campground");
    // Default: a J1772 station at 7 kW, with the honest note.
    await expect(night.getByLabel("Hookup at the stop")).toHaveValue("j1772");
    await expect(night.getByLabel("Planned power in kW")).toHaveValue("7");
    await expect(night.locator("[data-hookup-note]")).toContainText("planned at 7 kW (estimate)");
    // NEMA 14-50: the Portable Charger note, same 7.2 kW.
    await night.getByLabel("Hookup at the stop").selectOption("nema_14_50");
    await expect(night.locator("[data-hookup-note]")).toContainText("Up to 7.6 kW with Rivian's Portable Charger; planned at 7.2 kW (estimate) for campground voltage. You need to carry the Portable Charger.");
    await expect(night.getByLabel("Planned power in kW")).toHaveValue("7.2");
    // A slow outlet changes the power; the kW stays editable and in range.
    await night.getByLabel("Hookup at the stop").selectOption("tt_30");
    await expect(night.getByLabel("Planned power in kW")).toHaveValue("1.4");
    await expect(night.locator("[data-hookup-note]")).toContainText("needs an adapter not included with the truck");
    await night.getByLabel("Planned power in kW").fill("40");
    await expect(night.getByLabel("Planned power in kW")).toHaveValue("1.4");
    await night.getByLabel("Hookup at the stop").selectOption("nema_14_50");
    // No charger hides the power field.
    await night.getByLabel("Hookup at the stop").selectOption("none");
    await expect(night.getByLabel("Planned power in kW")).toHaveCount(0);
    await night.getByLabel("Hookup at the stop").selectOption("nema_14_50");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include("[data-overnight-stop]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `own-stop-${w}-dark.png`), fullPage: true });
    await ctx.close();
  });
}

test("the chosen hookup goes to the plan with its power", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const reqs: { days: { overnight?: { l2_kw: number; hookup?: string; name: string } }[] }[] = [];
  await open(page, async () => {
    await page.route("**/api/trips/plan-multiday", (r) => {
      reqs.push(r.request().postDataJSON());
      return r.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "not stubbed" }) });
    });
  });
  await page.locator("[data-overnight-stop]").first().getByLabel("Hookup at the stop").selectOption("nema_14_50");
  await page.getByRole("button", { name: "Plan 2-day trip" }).click();
  await expect.poll(() => reqs.length).toBe(1);
  expect(reqs[0].days[0].overnight).toMatchObject({ l2_kw: 7.2, hookup: "nema_14_50", name: expect.stringContaining("Mono Lake") });
  await ctx.close();
});
