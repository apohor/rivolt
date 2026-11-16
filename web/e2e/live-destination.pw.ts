import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState, type NowScenario, type StubOptions } from "./stub";

// Design "live_destination": while driving, the Live page's map shows where the truck's navigation is going (a pin and the planned route)
// and a row "To {name}: {n} mi left, arrive {time}". Off, or with no trip, it is the Live page users have today.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const FULL = JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8"));
async function stub(page: import("@playwright/test").Page, scenario: NowScenario, opts: StubOptions) {
  await stubApi(page, { scenario, liveSamples: true, ...opts });
  const state = { ...FULL, ...vehicleState(scenario) };
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(state) }));
}

test("a trip: the row says where, how far and when; the pin is on the map", async ({ page }) => {
  await stub(page, "driving", { designPreview: ["live_v2", "live_destination"], liveDestination: "trip" });
  await page.goto("/live");
  await expect(page.getByTestId("live-drive-map")).toBeVisible();
  await expect(page.getByTestId("live-destination")).toContainText(/^To Test Depot: 12 mi left, arrive \d{1,2}:\d{2}/);
  await expect(page.getByTitle("Destination: Test Depot")).toBeVisible();
});

test("the row follows the distance unit", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: "km" })));
  await stub(page, "driving", { designPreview: ["live_v2", "live_destination"], liveDestination: "trip" });
  await page.goto("/live");
  await expect(page.getByTestId("live-destination")).toContainText("20 km left");
  await expect(page.getByTestId("live-destination")).not.toContainText(" mi");
});

test("no trip (204): no row and no pin", async ({ page }) => {
  await stub(page, "driving", { designPreview: ["live_v2", "live_destination"], liveDestination: "none" });
  await page.goto("/live");
  await expect(page.getByTestId("live-drive-map")).toBeVisible();
  await page.waitForTimeout(800);
  await expect(page.getByTestId("live-destination")).toHaveCount(0);
  await expect(page.getByTitle(/^Destination:/)).toHaveCount(0);
});

test("feature off for the account (404): no row, and it asks once", async ({ page }) => {
  let asked = 0;
  page.on("request", (r) => { if (r.url().includes("/api/live-destination/")) asked++; });
  await stub(page, "driving", { designPreview: ["live_v2", "live_destination"] });
  await page.goto("/live");
  await expect(page.getByTestId("live-drive-map")).toBeVisible();
  await page.waitForTimeout(1500);
  await expect(page.getByTestId("live-destination")).toHaveCount(0);
  expect(asked).toBe(1);
});

test("design key off: the endpoint is never asked", async ({ page }) => {
  let asked = 0;
  page.on("request", (r) => { if (r.url().includes("/api/live-destination/")) asked++; });
  await stub(page, "driving", { designPreview: ["live_v2"], liveDestination: "trip" });
  await page.goto("/live");
  await expect(page.getByTestId("live-drive-map")).toBeVisible();
  await page.waitForTimeout(800);
  expect(asked).toBe(0);
  await expect(page.getByTestId("live-destination")).toHaveCount(0);
});

for (const [w, h] of [[393, 852], [1280, 900]] as const) {
  test(`shot live destination ${w}`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: h });
    await stub(page, "driving", { designPreview: ["live_v2", "live_destination"], liveDestination: "trip" });
    await page.goto("/live");
    await expect(page.getByTestId("live-destination")).toBeVisible();
    await page.waitForTimeout(1500);
    await page.getByTestId("live-drive-map").scrollIntoViewIfNeeded();
    await page.getByTestId("live-drive-map").screenshot({ path: path.join(SHOTS, `live-destination-${w}.png`) });
  });
}

test("no drive record yet (live-drive 204): the row and the pin are still there", async ({ page }) => {
  await stub(page, "driving", { designPreview: ["live_v2", "live_destination"], liveDestination: "trip" });
  await page.route(`**/api/live-drive/${VEHICLE_ID}`, (r) => r.fulfill({ status: 204 }));
  await page.goto("/live");
  await expect(page.getByText(/waiting for first frame/)).toBeVisible();
  await expect(page.getByTestId("live-destination")).toContainText(/^To Test Depot: 12 mi left/);
  await expect(page.getByTitle("Destination: Test Depot")).toBeVisible();
});

test("three failed reads in a row drop the trip; the next good read brings it back", async ({ page }) => {
  test.setTimeout(60_000);
  await stub(page, "driving", { designPreview: ["live_v2", "live_destination"], liveDestination: "trip" });
  let n = 0;
  await page.route(`**/api/live-destination/${VEHICLE_ID}`, (r) => {
    n++;
    if (n === 1 || n >= 6) return r.fulfill({ json: { name: "Test Depot", lat: 40.3, lon: -100.45, eta: new Date(Date.now() + 1_500_000).toISOString(), miles_left: 12.4, route: "_srtkA~lyh~D_cmA~hbE_etB~s`B", route_precision: 6 } });
    return r.fulfill({ status: 503, json: { error: "try again" } });
  });
  await page.goto("/live");
  await expect(page.getByTestId("live-destination")).toBeVisible();
  // Reads 2 and 3 failed: still shown. Read 4 is the third in a row.
  await expect(page.getByTestId("live-destination")).toHaveCount(0, { timeout: 30_000 });
  expect(n).toBeGreaterThanOrEqual(4);
  await expect(page.getByTestId("live-destination")).toBeVisible({ timeout: 20_000 });
});
