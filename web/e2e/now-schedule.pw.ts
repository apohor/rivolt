import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";

// Now: plugged in but not charging, with a schedule for today that applies here and has not
// started yet: "Plugged in · charging starts 11:00 PM (schedule)". Nothing otherwise.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

// Wednesday 9:00 PM in Chicago.
const NOW = new Date("2026-10-15T02:00:00Z");
const TZ = "America/Chicago";
const sched = (o: Record<string, unknown> = {}) => ({
  start_min: 23 * 60, duration_min: 480, enabled: true, weekdays: ["Wednesday", "Thursday"], amperage_a: 32, applies_here: true, ...o,
});

async function open(page: Page, schedules: unknown[] | undefined, charging = false) {
  await stubApi(page, { scenario: "parked" });
  const base = vehicleState(charging ? "charging" : "parked");
  const state = {
    ...base,
    ...(charging ? {} : { charger_status: "chrgr_sts_connected_no_chrg", charger_state: "charging_ready", battery_level_pct: 52, charge_target_pct: 80 }),
    ...(schedules ? { charging_schedules: schedules } : {}),
    at: new Date(NOW.getTime() - 20_000).toISOString(),
  };
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ json: state }));
  await page.clock.setFixedTime(NOW);
  await page.goto("/");
  await expect(page.getByTestId("now-headline")).toBeVisible();
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`schedule line · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme, timezoneId: TZ, locale: "en-US" });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await open(page, [sched()]);
      const line = page.getByTestId("now-headline");
      await expect(line).toContainText("Plugged in");
      await expect(page.getByTestId("now-schedule")).toHaveText(" · charging starts 11:00 PM (schedule)");
      await page.waitForTimeout(500);
      await page.screenshot({ path: path.join(SHOTS, `now-schedule-${w}-${theme}.png`) });
      const lines = await line.evaluate((el) => Math.round(el.getBoundingClientRect().height / (parseFloat(getComputedStyle(el).lineHeight) || 20)));
      expect(lines).toBeLessThanOrEqual(2);
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

const NONE: [string, unknown[] | undefined, boolean][] = [
  ["no schedules", undefined, false],
  ["applies elsewhere", [sched({ applies_here: false })], false],
  ["location unknown", [sched({ applies_here: null })], false],
  ["disabled", [sched({ enabled: false })], false],
  ["not today", [sched({ weekdays: ["Friday"] })], false],
  ["window already started", [sched({ start_min: 20 * 60 })], false],
  ["charging now", [sched()], true],
];
for (const [name, schedules, charging] of NONE) {
  test(`no schedule line: ${name}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, timezoneId: TZ, locale: "en-US" });
    const page = await ctx.newPage();
    await open(page, schedules, charging);
    await expect(page.getByTestId("now-schedule")).toHaveCount(0);
    await ctx.close();
  });
}
