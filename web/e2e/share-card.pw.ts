import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// The share sheet: masked by default (cost off every time it opens), generic file names, and nothing leaves the
// device: no request is made while opening, painting, saving or copying.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function openDemo(page: Page, url: string) {
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true });
  await page.goto(url);
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`month sheet: cost off by default, saves a generic file, makes no request · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark", acceptDownloads: true });
    const page = await ctx.newPage();
    await openDemo(page, "/demo/money");
    await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
    const requests: string[] = [];
    page.on("request", (r) => requests.push(r.url()));
    await page.getByRole("button", { name: "Share statement" }).click();
    const dlg = page.getByRole("dialog");
    await expect(dlg).toBeVisible();
    const cost = dlg.getByRole("checkbox", { name: /Include cost/ });
    await expect(cost).not.toBeChecked();
    await expect(dlg.getByRole("img", { name: "Preview of the card" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include("[data-share-sheet]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `share-sheet-${v.n}-dark.png`) });
    await cost.check();
    const download = page.waitForEvent("download");
    await dlg.getByRole("button", { name: "Save image" }).click();
    const file = await download;
    expect(file.suggestedFilename()).toMatch(/^rivolt-month-\d{4}-\d{2}\.png$/);
    await expect(dlg.getByRole("status")).toContainText("Saved rivolt-month-");
    // Nothing went anywhere: no request at all while the sheet was open and used.
    expect(requests).toEqual([]);
    // Closed and reopened, the toggle is off again.
    await dlg.getByRole("button", { name: "Cancel" }).click();
    await expect(dlg).toHaveCount(0);
    await page.getByRole("button", { name: "Share statement" }).click();
    await expect(page.getByRole("dialog").getByRole("checkbox", { name: /Include cost/ })).not.toBeChecked();
    await ctx.close();
  });
}

test("a drive offers Share trip, with a generic file name and no request", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark", acceptDownloads: true });
  const page = await ctx.newPage();
  // The new look's sticky page header is its own stacking context, which is where the nav used to cover the sheet.
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await openDemo(page, "/demo/activity");
  await page.locator('a[href^="/demo/drives/"]').first().click();
  await expect(page.getByRole("button", { name: "Share trip" })).toBeVisible();
  await page.waitForTimeout(1500);
  const requests: string[] = [];
  page.on("request", (r) => requests.push(r.url()));
  await page.getByRole("button", { name: "Share trip" }).click();
  const dlg = page.getByRole("dialog");
  await expect(dlg.getByRole("checkbox", { name: /Include cost/ })).not.toBeChecked();
  const download = page.waitForEvent("download");
  await dlg.getByRole("button", { name: "Save image" }).click();
  expect((await download).suggestedFilename()).toMatch(/^rivolt-trip-\d{4}-\d{2}\.png$/);
  expect(requests).toEqual([]);
  await page.screenshot({ path: path.join(SHOTS, "share-trip-iphone-dark.png") });
  // The sheet sits above the phone bottom nav, so Cancel is the top element at its centre and closes it.
  const box = await dlg.getByRole("button", { name: "Cancel" }).boundingBox();
  const top = await page.evaluate(([x, y]) => document.elementFromPoint(x, y)?.textContent, [box!.x + box!.width / 2, box!.y + box!.height / 2]);
  expect(top).toBe("Cancel");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  await expect(dlg).toHaveCount(0);
  await ctx.close();
});

test("a milestone offers Share, with no cost toggle (never a dollar figure) and a generic name", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark", acceptDownloads: true });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  await page.route("**/api/milestones", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ milestones: [{ vehicle_id: VEHICLE_ID, key: "miles_1000", kind: "miles_1000", achieved_at: "2026-10-17T12:00:00Z", title: "1,000 miles", body: "1,000 miles recorded in Rivolt: 2.50 mi/kWh, $0.04/mi" }] }) }),
  );
  await page.goto("/");
  await page.locator("[data-milestone]").getByRole("button", { name: "Share" }).click();
  const dlg = page.getByRole("dialog");
  await expect(dlg).toBeVisible();
  await expect(dlg.getByRole("checkbox")).toHaveCount(0);
  const download = page.waitForEvent("download");
  await dlg.getByRole("button", { name: "Save image" }).click();
  expect((await download).suggestedFilename()).toBe("rivolt-milestone-2026-10.png");
  await ctx.close();
});
