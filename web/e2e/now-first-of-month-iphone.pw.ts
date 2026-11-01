import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";

// The owner's report from the first day of a month on an iPhone: the battery chart's names lay on each other (one day of readings: its high and
// low, and the charge limit's name), the day's band was a sliver, a lone charged tick sat inside the pink low zone, and the footer's links were cut
// off behind the tab bar. One day of data, phone width: names apart, tick under the baseline, the last footer link above the tab bar.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const NOW = new Date(2026, 10, 1, 15, 0, 0);
const drive = { ID: "d-nov-1", VehicleID: VEHICLE_ID, StartedAt: new Date(2026, 10, 1, 8, 0).toISOString(), EndedAt: new Date(2026, 10, 1, 8, 40).toISOString(),
  StartSoCPct: 80, EndSoCPct: 65, StartOdometerMi: 100, EndOdometerMi: 110, DistanceMi: 10, StartLat: 40, StartLon: -100, EndLat: 40.1, EndLon: -100.1,
  MaxSpeedMph: 60, AvgSpeedMph: 35, EnergyUsedKWh: 3, Source: "live" };
const charge = { ID: "c-nov-1", VehicleID: VEHICLE_ID, StartedAt: new Date(2026, 10, 1, 9, 0).toISOString(), EndedAt: new Date(2026, 10, 1, 10, 0).toISOString(),
  StartSoCPct: 65, EndSoCPct: 80, EnergyAddedKWh: 20, MilesAdded: 50, MaxPowerKW: 11, AvgPowerKW: 10, FinalState: "complete", Lat: 40, Lon: -100, Source: "live",
  Cost: 3, Currency: "USD", PricePerKWh: 0.15, ChargeLimitPct: 80 };

async function open(page: Page, theme: "dark" | "light") {
  await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
  await stubApi(page);
  await page.clock.setFixedTime(NOW);
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  await page.route(/\/api\/drives(\?|$)/, (r) => r.fulfill(j([drive])));
  await page.route(/\/api\/charges(\?|$)/, (r) => r.fulfill(j([charge])));
  await page.goto("/");
}

for (const theme of ["dark", "light"] as const) {
  test(`iphone: the first of the month, one day of readings, names apart and the footer clear of the tab bar · ${theme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: theme });
    const page = await ctx.newPage();
    await open(page, theme);
    const chart = page.locator("[data-soc-trend]");
    await expect(chart).toBeVisible();
    await expect(chart.locator("[data-testid='soc-range']")).toHaveText("65–80% · Nov 1");
    // No two names overlap on screen.
    const boxes = await chart.locator("svg text[data-testid], svg [data-testid='soc-limit'] text").evaluateAll((els) =>
      els.map((e) => {
        const r = (e as SVGGraphicsElement).getBoundingClientRect();
        return { t: e.textContent, l: r.left, r: r.right, top: r.top, b: r.bottom };
      }),
    );
    for (let i = 0; i < boxes.length; i++)
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i], b = boxes[j];
        expect(a.l < b.r && b.l < a.r && a.top < b.b && b.top < a.b, `"${a.t}" and "${b.t}" overlap`).toBe(false);
      }
    // The charged tick hangs under the baseline.
    const tick = chart.locator("[data-draw='tick']").first();
    expect(Number(await tick.getAttribute("y2"))).toBeGreaterThan(Number(await tick.getAttribute("y1")));
    await chart.scrollIntoViewIfNeeded();
    await chart.screenshot({ path: path.join(SHOTS, `soc-one-day-iphone-${theme}.png`) });
    // The bottom of the page: every footer link is above the tab bar, the home indicator's room included.
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    await page.waitForTimeout(300);
    const tabTop = await page.locator("nav[aria-label='Primary']").last().evaluate((el) => el.getBoundingClientRect().top);
    const lastLink = await page.locator("footer a, footer span").last().evaluate((el) => el.getBoundingClientRect().bottom);
    expect(lastLink, "the footer's last line is behind the tab bar").toBeLessThanOrEqual(tabTop + 0.5);
    await page.screenshot({ path: path.join(SHOTS, `footer-bottom-iphone-${theme}.png`) });
    await ctx.close();
  });
}
