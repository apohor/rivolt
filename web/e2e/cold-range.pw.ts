import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// Truck tab "Range at different temperatures": the user's own band near a temperature, a chip per temperature (greyed
// where there is too little history), honest sentences, and nothing on a truck that can't be tracked.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const base = { units: "imperial", distance_unit: "mi", efficiency_unit: "mi/kWh", min_drives: 5, min_miles: 50 };
const AVAILABLE = [4.4, 15.6, 26.7]; // 40, 60, 80 °F
const fig = (t: number, range = true) => ({
  ...base, temp_c: t, efficiency_low: 2.4, efficiency_high: 2.8, range_low: range ? 190 : null, range_high: range ? 215 : null,
  based_on: { drives: 14, miles: 312, temp_lo_c: t - 4, temp_hi_c: t + 4 }, available: AVAILABLE,
});
const sparse = (t: number) => ({ ...base, temp_c: t, reason: "not_enough_data", efficiency_low: null, efficiency_high: null, range_low: null, range_high: null, based_on: null, available: AVAILABLE });

async function open(page: Page, answer: (t: number) => { status?: number; body: unknown }) {
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.ui.next", "on");
    localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ temperatureUnit: "f", temperatureUnitSet: true, distanceUnit: "mi" }));
  });
  await stubApi(page);
  const asked: number[] = [];
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/cold-range`), (r) => {
    const t = Number(new URL(r.request().url()).searchParams.get("temp_c"));
    asked.push(t);
    const a = answer(t);
    return r.fulfill({ status: a.status ?? 200, contentType: "application/json", body: JSON.stringify(a.body) });
  });
  await page.goto("/truck");
  await expect(page.getByText("Pack capacity").first()).toBeVisible();
  return asked;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`a figure, chips greyed where there is no history, and a chip changes the question · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const asked = await open(page, (t) => ({ body: AVAILABLE.some((a) => Math.abs(a - t) < 0.3) ? fig(t) : sparse(t) }));
    const card = page.locator("[data-cold-range]");
    // Starts on the coldest chip that has history (40 °F here).
    await expect(card).toContainText("Around 40 °F, a full charge usually gives about 190–215 mi, from your own drives (estimate).");
    await expect(card).toContainText("The temperature comes from weather data, not from your truck.");
    await expect(card.getByRole("button", { name: "0 °F", exact: true })).toBeDisabled();
    await expect(card.getByRole("button", { name: "20 °F", exact: true })).toBeDisabled();
    await expect(card.getByRole("button", { name: "40 °F", exact: true })).toHaveAttribute("aria-pressed", "true");
    await expect(card).not.toContainText(/%|better|worse|other (drivers|owners)/i);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await card.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `cold-range-${v.n}-dark.png`), fullPage: true });
    await card.getByRole("button", { name: "80 °F", exact: true }).click();
    await expect(card.getByRole("button", { name: "80 °F", exact: true })).toHaveAttribute("aria-pressed", "true");
    expect(asked.some((t) => Math.abs(t - 26.7) < 0.3)).toBe(true);
    await ctx.close();
  });
}

test("no range without a pack: the mi/kWh band alone; too little history says what it needs", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, (t) => ({ body: fig(t, false) }));
  await expect(page.locator("[data-cold-range]")).toContainText("your drives average about 2.4–2.8 mi/kWh (estimate)");
  const p2 = await ctx.newPage();
  await open(p2, (t) => ({ body: { ...sparse(t), available: [] } }));
  await expect(p2.locator("[data-cold-range]")).toContainText("Not enough drives near 40 °F yet. It needs about 5 drives and 50 mi near that temperature.");
  await ctx.close();
});

test("a truck that can't be tracked says so; a server without it shows no card", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const a = await ctx.newPage();
  await open(a, () => ({ body: { ...base, temp_c: 4.4, reason: "not_tracked", efficiency_low: null, efficiency_high: null, range_low: null, range_high: null, based_on: null, available: [] } }));
  await expect(a.locator("[data-cold-range]")).toContainText("Can't be tracked on this truck yet.");
  await expect(a.locator("[data-cold-range]").getByRole("button")).toHaveCount(0);
  const b = await ctx.newPage();
  await open(b, () => ({ status: 404, body: { error: "not found" } }));
  await b.waitForTimeout(800);
  await expect(b.locator("[data-cold-range]")).toHaveCount(0);
  await ctx.close();
});
