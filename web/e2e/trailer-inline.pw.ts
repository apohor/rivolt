import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// trailer_inline (design preview): the Trailer card is gone; one line under the title says "Trailer: none" or "Trailer: yes (not in efficiency)",
// and "change" opens a small menu of the same three choices. Flag off: the card as before. Shots at 393 and 1280, light and dark.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ago = (days: number) => new Date(e2eNow() - days * 86_400_000).toISOString();
const drive = (id: string, days: number, o: Record<string, unknown> = {}) => ({
  ID: id, VehicleID: VEHICLE_ID, StartedAt: ago(days), EndedAt: ago(days - 0.02), StartSoCPct: 70, EndSoCPct: 60, StartOdometerMi: 100, EndOdometerMi: 130,
  DistanceMi: 30, StartLat: 40, StartLon: -100, EndLat: 40.4, EndLon: -100.6, MaxSpeedMph: 70, AvgSpeedMph: 40, EnergyUsedKWh: 12, Source: "live", RoutePolyline: "", ...o,
});
const DRIVES = [drive("plain", 3), drive("towing", 2, { Trailer: true })];

async function open(page: Page, id: string, flag: boolean) {
  await stubApi(page, { scenario: "parked", designPreview: flag ? ["trailer_inline"] : [] });
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  await page.route(/\/api\/drives(\?|$)/, (r) => r.fulfill(j(DRIVES)));
  await page.route(/\/api\/drives\/[^/?]+$/, (r) => {
    const d = DRIVES.find((x) => x.ID === new URL(r.request().url()).pathname.split("/").pop());
    return d ? r.fulfill(j(d)) : r.fulfill({ status: 404, contentType: "application/json", body: "{}" });
  });
  await page.route(/\/api\/samples/, (r) => r.fulfill(j([])));
  await page.addInitScript(() => localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 })));
  await page.goto(`/drives/${id}`);
}

for (const [vpName, vp] of [["393", { width: 393, height: 852 }], ["1280", { width: 1280, height: 900 }]] as const) {
  for (const scheme of ["light", "dark"] as const) {
    test(`trailer line replaces the card · ${vpName} · ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), scheme);
      await open(page, "towing", true);
      const line = page.locator("[data-trailer-inline]");
      await expect(line).toContainText("Trailer: yes (not in efficiency)");
      await expect(page.locator("[data-trailer-control]")).toHaveCount(0);
      await page.locator("[data-trailer-change]").click();
      const menu = page.getByRole("menu", { name: "Trailer" });
      await expect(menu.getByRole("menuitemradio")).toHaveCount(3);
      await expect(menu.getByRole("menuitemradio", { name: /Automatic/ })).toHaveAttribute("aria-checked", "true");
      await page.screenshot({ path: path.join(SHOTS, `trailer-inline-open-${vpName}-${scheme}.png`) });
      let body: unknown = null;
      await page.route(/\/api\/drives\/towing\/trailer$/, (r) => {
        body = r.request().postDataJSON();
        return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ trailer_override: false }) });
      });
      await menu.getByRole("menuitemradio", { name: "No trailer" }).click();
      await expect.poll(() => body).toEqual({ trailer: false });
      await expect(menu).toHaveCount(0);
      await page.screenshot({ path: path.join(SHOTS, `trailer-inline-${vpName}-${scheme}.png`) });
      await ctx.close();
    });

    test(`flag off keeps the card · ${vpName} · ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), scheme);
      await open(page, "towing", false);
      await expect(page.locator("[data-trailer-control]")).toHaveCount(1);
      await expect(page.locator("[data-trailer-inline]")).toHaveCount(0);
      await page.screenshot({ path: path.join(SHOTS, `trailer-card-${vpName}-${scheme}.png`), fullPage: true });
      await ctx.close();
    });
  }
}

test("a drive with no trailer says none", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, "plain", true);
  await expect(page.locator("[data-trailer-inline]")).toContainText("Trailer: none");
  await ctx.close();
});
