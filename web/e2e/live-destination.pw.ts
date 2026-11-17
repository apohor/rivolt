import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState, type NowScenario, type StubOptions } from "./stub";

// While driving, the Live page's map shows where the truck's navigation is going (a pin and the planned route) and a row
// "To {name}: {n} mi left, arrive {time}". It follows the endpoint's own answer (404 = off for the account), not a design key.
// No trip, or off, is the Live page users have today.
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

test("no design keys at all: a trip still shows its row and map", async ({ page }) => {
  await stub(page, "driving", { designPreview: [], liveDestination: "trip" });
  await page.goto("/live");
  await expect(page.getByTestId("live-destination")).toContainText(/^To Test Depot: 12 mi left/);
  await expect(page.getByTestId("live-drive-map")).toBeVisible();
});

test("no design keys, feature off: no map, and it asks once", async ({ page }) => {
  let asked = 0;
  let sampled = 0;
  page.on("request", (r) => {
    if (r.url().includes("/api/live-destination/")) asked++;
    if (r.url().includes("/api/samples")) sampled++;
  });
  await stub(page, "driving", { designPreview: [] });
  await page.goto("/live");
  await expect(page.getByText("Max speed")).toBeVisible();
  await page.waitForTimeout(1500);
  await expect(page.getByTestId("live-drive-map")).toHaveCount(0);
  await expect(page.getByTestId("live-destination")).toHaveCount(0);
  expect(asked).toBe(1);
  // Nothing is drawn, so the trail is not fetched either.
  expect(sampled).toBe(0);
});

test("no design keys, no trip (204): no map", async ({ page }) => {
  await stub(page, "driving", { designPreview: [], liveDestination: "none" });
  await page.goto("/live");
  await page.waitForTimeout(1000);
  await expect(page.getByTestId("live-drive-map")).toHaveCount(0);
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
  await expect(page.getByTestId("live-destination")).toContainText(/^To Test Depot: 12 mi left/);
  await expect(page.getByTitle("Destination: Test Depot")).toBeVisible();
  // The map is up, so the "waiting" line goes.
  await expect(page.getByText(/waiting for first frame/)).toHaveCount(0);
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
  await expect.poll(() => n, { timeout: 20_000 }).toBeGreaterThanOrEqual(3);
  await expect(page.getByTestId("live-destination")).toBeVisible();
  await expect(page.getByTestId("live-destination")).toHaveCount(0, { timeout: 30_000 });
  expect(n).toBeGreaterThanOrEqual(4);
  await expect(page.getByTestId("live-destination")).toBeVisible({ timeout: 20_000 });
});

test("feature off: the waiting card keeps its line and draws no map", async ({ page }) => {
  await stub(page, "driving", { designPreview: ["live_v2"] });
  await page.route(`**/api/live-drive/${VEHICLE_ID}`, (r) => r.fulfill({ status: 204 }));
  await page.goto("/live");
  await expect(page.getByText(/waiting for first frame/)).toBeVisible();
  await page.waitForTimeout(800);
  await expect(page.getByTestId("live-drive-map")).toHaveCount(0);
});
