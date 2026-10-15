import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Signed-out demo: receipts and Activity rows open working, read-only
// detail views under /demo, and back / refresh / a fresh open all work.

async function openDemo(page: Page, path: string) {
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true });
  await page.goto(path);
}

test("a Money receipt opens a read-only charge detail; back and refresh work", async ({ page }) => {
  await openDemo(page, "/demo?tab=money");
  await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
  const link = page.locator('a[href^="/demo/charges/"]').first();
  await expect(link).toBeVisible();
  await link.click();
  await expect(page).toHaveURL(/\/demo\/charges\//);
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  // Read-only: no price editing, no delete.
  await expect(page.getByText("Delete this charge")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Mark as free" })).toHaveCount(0);
  await expect(page.getByText("Save pricing")).toHaveCount(0);
  // The sample banner is still there.
  await expect(page.getByText("Sample truck.")).toBeVisible();
  // Refresh on the detail route.
  await page.reload();
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  // Back lands on the same tab.
  await page.getByRole("link", { name: /Back to the demo/ }).click();
  await expect(page).toHaveURL(/\/demo$/);
});

test("Activity drive and charge cards open demo detail routes", async ({ page }) => {
  await openDemo(page, "/demo?tab=activity");
  const drive = page.locator('a[href^="/demo/drives/"]').first();
  await expect(drive).toBeVisible();
  await drive.click();
  await expect(page).toHaveURL(/\/demo\/drives\//);
  await expect(page.getByRole("heading", { name: /Drive/ }).first()).toBeVisible();
  await expect(page.getByText("Sample truck.")).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/\/demo\?tab=activity/);
  const charge = page.locator('a[href^="/demo/charges/"]').first();
  await expect(charge).toBeVisible();
  await charge.click();
  await expect(page).toHaveURL(/\/demo\/charges\//);
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
});

test("a fresh open of a demo detail URL works signed out", async ({ page }) => {
  await openDemo(page, "/demo");
  await page.getByRole("button", { name: "Activity" }).click();
  const href = await page.locator('a[href^="/demo/charges/"]').first().getAttribute("href");
  expect(href).toBeTruthy();
  await page.goto(href!);
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  // Never bounced to the login page.
  expect(new URL(page.url()).pathname).not.toBe("/login");
});

test("the demo banner CTA goes to signup, or to login when sign-ups are closed", async ({ page }) => {
  await openDemo(page, "/demo");
  await expect(page.getByRole("link", { name: "Connect with a driver account" })).toHaveAttribute("href", "/signup");
});

test("closed sign-ups send the demo CTA to login", async ({ page }) => {
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true, signupDisabled: true });
  await page.goto("/demo");
  await expect(page.getByRole("link", { name: "Connect with a driver account" })).toHaveAttribute("href", "/login");
});

test("the demo never requests the chargers archive or routing", async ({ page }) => {
  const tileReqs: string[] = [];
  page.on("request", (r) => {
    // No tile archives and no anonymous routing/map-matching calls.
    // The basemap archive (us.pmtiles) is allowed for the demo; the
    // chargers archive and anonymous routing are not.
    if (r.url().includes("chargers.pmtiles") || r.url().includes("/api/maps/valhalla")) tileReqs.push(r.url());
  });
  await openDemo(page, "/demo?tab=activity");
  await page.locator('a[href^="/demo/drives/"]').first().click();
  await expect(page.getByRole("heading", { name: /Drive/ }).first()).toBeVisible();
  await page.waitForTimeout(1500);
  await page.goBack();
  await page.locator('a[href^="/demo/charges/"]').first().click();
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  await page.waitForTimeout(1500);
  expect(tileReqs).toEqual([]);
});

test("the demo's Activity has no sign-in-only table links", async ({ page }) => {
  await openDemo(page, "/demo?tab=activity");
  await expect(page.getByRole("heading", { name: "Activity" })).toBeVisible();
  await expect(page.locator('a[href="/drives"], a[href="/charges"]')).toHaveCount(0);
});

test("Share statement gives visible feedback in the demo", async ({ page }) => {
  await openDemo(page, "/demo?tab=money");
  const share = page.getByRole("button", { name: "Share statement" });
  await expect(share).toBeEnabled();
  const download = page.waitForEvent("download");
  await share.click();
  await download;
  await expect(page.getByRole("status").filter({ hasText: /Saved rivolt-.*\.png/ })).toBeVisible();
});

test("browser back from a demo drive detail throws no page error", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await openDemo(page, "/demo?tab=activity");
  await page.locator('a[href^="/demo/drives/"]').first().click();
  await expect(page.getByRole("heading", { name: /Drive/ }).first()).toBeVisible();
  // Back immediately, while the map's deferred layout callbacks are pending.
  await page.goBack();
  await expect(page).toHaveURL(/\/demo\?tab=activity/);
  await page.waitForTimeout(800);
  expect(errors.filter((m) => /LatLng/.test(m))).toEqual([]);
});

test("the demo's ledger calls use the fixed Pacific zone, whatever the viewer's", async ({ browser }) => {
  // A viewer in Tokyo still gets the demo's month in the sample truck's zone.
  const ctx = await browser.newContext({ timezoneId: "Asia/Tokyo", viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  const tzs: string[] = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/ledger/")) tzs.push(u.searchParams.get("tz") ?? "");
  });
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true });
  await page.goto("/demo?tab=money");
  await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
  await expect(page.getByText("Times shown in Pacific time.")).toBeVisible();
  const ledger = tzs.filter((t) => t !== "");
  expect(ledger.length).toBeGreaterThan(0);
  expect(new Set(ledger)).toEqual(new Set(["America/Los_Angeles"]));
  await ctx.close();
});

test("the demo follows the OS theme, set before the app bundle loads", async ({ browser }) => {
  for (const [os, expectLight] of [["light", true], ["dark", false]] as const) {
    const ctx = await browser.newContext({ colorScheme: os, viewport: { width: 390, height: 844 } });
    const page = await ctx.newPage();
    // Block the app bundle: only theme-init.js can have set the attribute.
    await page.route("**/assets/*.js", (r) => r.abort());
    await page.goto("/demo", { waitUntil: "domcontentloaded" });
    if (expectLight) await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    else await expect(page.locator("html")).not.toHaveAttribute("data-theme", "light");
    await ctx.close();
  }
});

test("the demo's money grammar stays readable in light mode", async ({ browser }) => {
  const ctx = await browser.newContext({ colorScheme: "light", viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  await openDemo(page, "/demo?tab=money");
  await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  // Hero total, split-bar legend and a receipt's source label all
  // contrast with the page (luminance gap, same check as the Money view).
  const lum = (c: string) => {
    const [r, g, b] = (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number);
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  for (const sel of ["div.text-4xl", "dl dt", "text=/Billed|Estimate|Home rate/"]) {
    const el = page.locator(sel).first();
    if ((await el.count()) === 0) continue;
    const fg = await el.evaluate((n) => getComputedStyle(n).color);
    expect(Math.abs(lum(fg) - lum(bg)), sel).toBeGreaterThan(60);
  }
  await ctx.close();
});

test("Activity filters in the demo keep the Activity tab and the filter sticks", async ({ page }) => {
  await openDemo(page, "/demo?tab=activity");
  await expect(page.getByRole("heading", { name: "Activity" })).toBeVisible();
  await page.getByRole("tab", { name: "Drives" }).click();
  await expect(page).toHaveURL(/tab=activity/);
  await expect(page).toHaveURL(/f=drives/);
  await expect(page.getByRole("heading", { name: "Activity" })).toBeVisible();
  await expect(page.locator('a[href^="/demo/charges/"]')).toHaveCount(0);
  await expect(page.locator('a[href^="/demo/drives/"]').first()).toBeVisible();
  await page.getByRole("tab", { name: "Charges" }).click();
  await expect(page).toHaveURL(/tab=activity/);
  await expect(page).toHaveURL(/f=charges/);
  await expect(page.locator('a[href^="/demo/drives/"]')).toHaveCount(0);
  await expect(page.locator('a[href^="/demo/charges/"]').first()).toBeVisible();
  await page.getByRole("tab", { name: "All" }).click();
  await expect(page).toHaveURL(/tab=activity/);
  await expect(page).not.toHaveURL(/f=/);
});

test("the demo map uses the self-hosted basemap, never CARTO's keyed raster", async ({ page }) => {
  const reqs: string[] = [];
  page.on("request", (r) => reqs.push(r.url()));
  await openDemo(page, "/demo?tab=activity");
  await page.locator('a[href^="/demo/charges/"]').first().click();
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  await page.waitForTimeout(1500);
  expect(reqs.filter((u) => u.includes("basemaps.cartocdn.com"))).toEqual([]);
  // The self-hosted basemap archive is what the map reads.
  expect(reqs.some((u) => u.includes("/api/maps/tiles/us.pmtiles"))).toBe(true);
});

test("without a basemap archive the map says so, and calls no third-party tile host", async ({ page }) => {
  const reqs: string[] = [];
  page.on("request", (r) => reqs.push(r.url()));
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true, noTiles: true });
  await page.goto("/demo?tab=activity");
  await page.locator('a[href^="/demo/charges/"]').first().click();
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  await expect(page.getByText("Map tiles unavailable on this server").first()).toBeVisible();
  await expect(page.getByText("© OpenStreetMap contributors").first()).toBeVisible();
  expect(reqs.filter((u) => /cartocdn\.com|openstreetmap\.org\/\d/.test(u))).toEqual([]);
});
