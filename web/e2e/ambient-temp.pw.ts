import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// Outside temperature is off until the user turns it on: the Settings switch
// and the reading once it is on. Nothing is asked or shown in the demo.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
const j = (b: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(b) });
const TEXT = "Outside temperature: Rivian doesn't report it, so Rivolt looks it up from Open-Meteo using your truck's location rounded to about 11 km, at most every 30 minutes and only while you have Rivolt open. Off by default.";

async function open(page: Page, o: { enabled: boolean }) {
  await stubApi(page, { nowEndpoint: true });
  const puts: { enabled: boolean }[] = [];
  let enabled = o.enabled;
  await page.route("**/api/settings/ambient-temp", (r) => {
    if (r.request().method() === "PUT") {
      const b = r.request().postDataJSON() as { enabled: boolean };
      puts.push(b);
      enabled = b.enabled;
    }
    return r.fulfill(j({ enabled }));
  });
  // /now carries the reading only once it is on.
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/now`), (r) => {
    const iso = (ago: number) => new Date(e2eNow() - ago).toISOString();
    return r.fulfill(
      j({
        version: 1, at: iso(8_000), location_fix_at: iso(3 * 3_600_000), connection: "replay", state: "parked", state_since: iso(2 * 3_600_000),
        soc_at_state_start: 68, tire_pressure_min_bar: 2.8, failed_sleeps_6h: 0, charge: null,
        ...(enabled ? { ambient: { temp_c: 7.4, source: "open-meteo", as_of: iso(60_000) } } : {}),
      }),
    );
  });
  return puts;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`Settings: the switch is off by default, shows the approved text, and saves · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const puts = await open(page, { enabled: false });
    await page.goto("/settings?tab=account");
    const sw = page.getByRole("switch", { name: "Show outside temperature" });
    await expect(sw).toHaveAttribute("aria-checked", "false");
    await expect(page.getByText(TEXT)).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.locator("#ambient-temp").scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `ambient-settings-${v.n}-dark.png`) });
    await sw.click();
    await expect(sw).toHaveAttribute("aria-checked", "true");
    expect(puts).toEqual([{ enabled: true }]);
    await ctx.close();
  });
}

test("the demo never asks for, or offers, outside temperature", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  const calls: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname.includes("ambient-temp")) calls.push(r.url());
  });
  await page.goto("/demo");
  await page.waitForTimeout(1200);
  await expect(page.locator("[data-ambient-offer], [data-ambient-temp]")).toHaveCount(0);
  expect(calls).toEqual([]);
});

