import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The off-peak schedule editor in Settings > Charging: several windows, weekday/weekend splits, a rate each, in one time zone, with errors next to their row.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

type Put = { tz: string; windows: { days: string; start: string; end: string; rate: number; label?: string }[] };

async function open(page: Page, initial: Put, puts: Put[], fail?: string) {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  await page.route("**/api/settings/charging/offpeak", (r) => {
    if (r.request().method() === "PUT") {
      const body = r.request().postDataJSON() as Put;
      puts.push(body);
      if (fail) return r.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ error: fail }) });
      return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
    }
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(initial) });
  });
  await page.goto("/settings?tab=charging");
  await expect(page.locator("[data-offpeak]")).toBeVisible();
}

const TZ = "America/Chicago";
for (const v of [
  { w: 390, h: 844, scheme: "dark" as const },
  { w: 1280, h: 900, scheme: "light" as const },
]) {
  test(`edit windows: weekday and weekend splits, errors by row, save · ${v.w}px ${v.scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: v.w, height: v.h }, colorScheme: v.scheme });
    const page = await ctx.newPage();
    const puts: Put[] = [];
    await open(page, { tz: TZ, windows: [{ days: "weekdays", start: "21:00", end: "06:00", rate: 0.09, label: "Overnight" }] }, puts);
    const form = page.locator("[data-offpeak]");
    await expect(form).toContainText("Times are in");
    await expect(form.getByLabel("Times are in")).toHaveValue(TZ);
    await expect(form.locator("[data-offpeak-window]")).toHaveCount(1);
    await expect(form.getByLabel(/Rate per kWh/)).toHaveValue("0.09");
    await expect(form.getByRole("button", { name: "Save schedule" })).toBeDisabled();

    // A weekend window, then an overlap with the weekday one on the second row.
    await form.getByRole("button", { name: "Add a window" }).click();
    const second = form.locator("[data-offpeak-window]").nth(1);
    await second.getByLabel("Days").selectOption("weekends");
    await second.getByLabel("From").fill("08:00");
    await second.getByLabel("To").fill("20:00");
    await second.getByLabel(/Rate per kWh/).fill("0.07");
    await expect(second.getByRole("alert")).toHaveCount(0);
    // Friday night's window runs into Saturday morning, so a weekend window from 5 AM overlaps it.
    await second.getByLabel("From").fill("05:00");
    await expect(second.getByRole("alert")).toContainText("Overlaps window 1 (Weekdays, 9 PM to 6 AM)");
    await expect(form.getByRole("button", { name: "Save schedule" })).toBeDisabled();
    await second.getByLabel("From").fill("08:00");

    // Rates out of range and too precise.
    await second.getByLabel(/Rate per kWh/).fill("6");
    await expect(second.getByRole("alert")).toContainText("between 0 and 5");
    await second.getByLabel(/Rate per kWh/).fill("0.123456");
    await expect(second.getByRole("alert")).toContainText("at most 4 decimal places");
    await second.getByLabel(/Rate per kWh/).fill("0.07");
    await second.getByLabel("To").fill("08:00");
    await expect(second.getByRole("alert")).toContainText("can't be the same time");
    await second.getByLabel("To").fill("20:00");

    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await expect(form.locator("[data-offpeak-strips]")).toBeVisible();
    expect((await new AxeBuilder({ page }).include("[data-offpeak]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await form.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `offpeak-${v.w}-${v.scheme}.png`), fullPage: true });

    await form.getByRole("button", { name: "Save schedule" }).click();
    await expect(form.getByRole("status")).toHaveText("Saved.");
    expect(puts).toHaveLength(1);
    expect(puts[0].tz).toBe(TZ);
    expect(puts[0].windows).toEqual([
      { days: "weekdays", start: "21:00", end: "06:00", rate: 0.09, label: "Overnight" },
      { days: "weekends", start: "08:00", end: "20:00", rate: 0.07 },
    ]);
    await ctx.close();
  });
}

test("removing every window clears the schedule; a server refusal shows its words", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const puts: Put[] = [];
  await open(page, { tz: TZ, windows: [{ days: "all", start: "22:00", end: "05:00", rate: 0.1 }] }, puts, "window 1 overlaps another window");
  const form = page.locator("[data-offpeak]");
  await form.getByRole("button", { name: "Remove window 1" }).click();
  await expect(form).toContainText("No off-peak windows. Every home charge is priced at your home rate.");
  await form.getByRole("button", { name: "Save schedule" }).click();
  expect(puts[0].windows).toEqual([]);
  await expect(form).toContainText("window 1 overlaps another window");
  await ctx.close();
});
