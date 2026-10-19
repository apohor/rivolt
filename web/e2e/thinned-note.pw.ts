import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// A drive whose month has been thinned says so once, under the header, and links to the backup download. Nothing on a
// full-detail drive or in the demo.
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
const base = {
  ID: "drive-old", VehicleID: "v", StartedAt: "2026-06-01T14:00:00Z", EndedAt: "2026-06-01T14:30:00Z", StartSoCPct: 70, EndSoCPct: 66, StartOdometerMi: 1000, EndOdometerMi: 1012, DistanceMi: 12,
  StartLat: 40, StartLon: -100, EndLat: 40.1, EndLon: -100.1, MaxSpeedMph: 60, AvgSpeedMph: 35, EnergyUsedKWh: 4, Source: "live",
};

async function open(page: Page, thinned: boolean | undefined) {
  await stubApi(page);
  const row = { ...base, ...(thinned === undefined ? {} : { thinned }) };
  await page.route(/\/api\/drives(\?|$)/, (r) => r.fulfill(j([row])));
  await page.route(/\/api\/drives\/drive-old(\?|$)/, (r) => r.fulfill(j(row)));
  await page.goto("/drives/drive-old");
  await expect(page.getByRole("heading", { name: /Drive/ }).first()).toBeVisible();
}

for (const w of [390, 1280] as const) {
  test(`a thinned drive shows one line with a link to the backup download · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, true);
    const note = page.locator("[data-thinned-note]");
    await expect(note).toContainText("This drive is older than 90 days, so its route and charts are drawn from a lighter recording. The full detail is in your backup download.");
    await expect(note.getByRole("link", { name: "backup download" })).toHaveAttribute("href", "/settings?tab=data");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include("[data-thinned-note]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await ctx.close();
  });
}

test("no note when the drive has full detail or the server doesn't say", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  for (const t of [false, undefined]) {
    const page = await ctx.newPage();
    await open(page, t);
    await page.waitForTimeout(600);
    await expect(page.locator("[data-thinned-note]")).toHaveCount(0);
  }
  await ctx.close();
});

test("never in the demo", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true });
  await page.route(/\/api\/drives(\?|$)/, (r) => r.fulfill(j([{ ...base, thinned: true }])));
  await page.goto("/demo/drives/drive-old");
  await page.waitForTimeout(1000);
  await expect(page.locator("[data-thinned-note]")).toHaveCount(0);
  await ctx.close();
});
