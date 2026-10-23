import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Activity: Feed | Table | Map switcher (?view=), reusing the
// Drives and Charges tables and overview maps. Flag off is unchanged.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function open(page: Page, demo = false) {
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.theme", "dark");
    // The stub's rows are a few hours to days old: show all time.
    localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
  });
  await stubApi(page, demo ? { demo: true, signedIn: false } : {});
}

for (const vp of [
  { name: "390", size: { width: 390, height: 844 } },
  { name: "1280", size: { width: 1280, height: 900 } },
]) {
  test(`next: Feed, Table and Map views render at ${vp.name}`, async ({ page }) => {
    await page.setViewportSize(vp.size);
    await open(page);
    await page.goto("/activity");
    const views = page.getByRole("group", { name: "View" });
    await expect(views).toBeVisible();
    await expect(views.getByRole("button", { name: "Feed", pressed: true })).toBeVisible();
    await expect(page.locator("[data-ui-feed]")).toBeVisible();
    await expect(page.getByText("Tables:")).toHaveCount(0);

    await views.getByRole("button", { name: "Table" }).click();
    await expect(page).toHaveURL(/view=table/);
    await expect(page.locator("[data-activity-view='table'] table")).toHaveCount(2);
    await expect(page.locator("[data-activity-view='table'] table").first()).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS, `activity-table-${vp.name}.png`) });

    // The filter keeps the view; Drives shows only the drive table.
    await page.getByRole("tab", { name: "Drives" }).click();
    await expect(page).toHaveURL(/view=table/);
    await expect(page).toHaveURL(/f=drives/);
    await expect(page.locator("[data-activity-view='table'] table")).toHaveCount(1);
    await page.getByRole("tab", { name: "All" }).click();

    await views.getByRole("button", { name: "Map" }).click();
    await expect(page).toHaveURL(/view=map/);
    // One map at a time on All: a Drives | Charges layer toggle instead of two stacked maps.
    await expect(page.locator("[data-activity-view='map'] .leaflet-container")).toHaveCount(1);
    await expect(page.locator("[data-activity-view='map'] .leaflet-container").first()).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS, `activity-map-${vp.name}.png`) });
    await page.getByRole("group", { name: "Map layer" }).getByRole("button", { name: /Charges/ }).click();
    await expect(page.locator("[data-activity-view='map'] section[aria-label='Charges'] .leaflet-container")).toBeVisible();
    await expect(page.locator("[data-activity-view='map'] section[aria-label='Drives']")).toHaveCount(0);

    await views.getByRole("button", { name: "Feed" }).click();
    await expect(page).not.toHaveURL(/view=/);
    await expect(page.locator("[data-ui-feed]")).toBeVisible();
  });
}

test("next: a table row opens its drive; ?view survives a reload", async ({ page }) => {
  await open(page);
  await page.goto("/activity?view=table");
  await expect(page.locator("[data-activity-view='table'] table").first()).toBeVisible();
  await page.reload();
  await expect(page.getByRole("button", { name: "Table", pressed: true })).toBeVisible();
  await page.getByRole("link", { name: /Open drive from/ }).first().click();
  await expect(page).toHaveURL(/\/drives\/[^/]+$/);
});

test("demo: tables render read-only and keep tab=activity", async ({ page }) => {
  await open(page, true);
  await page.goto("/demo/activity?view=table");
  await expect(page.locator("[data-activity-view='table'] table").first()).toBeVisible();
  await page.getByRole("group", { name: "View" }).getByRole("button", { name: "Map" }).click();
  await expect(page).toHaveURL(/\/demo\/activity\?view=map/);
  await expect(page.locator("[data-activity-view='map'] .leaflet-container").first()).toBeVisible();
});
