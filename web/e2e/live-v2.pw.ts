import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState, type NowScenario, type StubOptions } from "./stub";

// Design "live_v2": the Live page draws the open charge as a graph with the pack temperature, and the drive in progress on a map. Off, or
// when neither is happening, it is the Live page users have today.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

// The Live page needs the full state a truck reports (the default stub is the slim Now one); the fixture is the iPhone app's.
const FULL = JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8"));
async function stub(page: import("@playwright/test").Page, scenario: NowScenario, opts: StubOptions) {
  await stubApi(page, { scenario, liveSamples: true, ...opts });
  const state = { ...FULL, ...vehicleState(scenario) };
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(state) }));
}

test("live page, live_v2 off: no graph and no map while charging or driving", async ({ page }) => {
  for (const scenario of ["charging", "driving"] as const) {
    await stub(page, scenario, { designPreview: [] });
    await page.goto("/live");
    await expect(page.getByRole("heading", { name: "Live" })).toBeVisible();
    await page.waitForTimeout(600);
    await expect(page.getByTestId("live-charge-graph")).toHaveCount(0);
    await expect(page.getByTestId("live-drive-map")).toHaveCount(0);
  }
});

test("live page, live_v2 on while charging: the graph and the pack temperature", async ({ page }) => {
  await stub(page, "charging", { designPreview: ["live_v2"] });
  await page.goto("/live");
  const graph = page.getByTestId("live-charge-graph");
  await expect(graph).toBeVisible();
  await expect(page.getByTestId("live-pack-temp")).toContainText("Pack");
  await expect(page.getByTestId("live-pack-temp")).toContainText("°");
  await expect(page.getByTestId("live-drive-map")).toHaveCount(0);
});

test("live page, live_v2 on while parked: neither", async ({ page }) => {
  await stub(page, "parked", { designPreview: ["live_v2"] });
  await page.goto("/live");
  await expect(page.getByRole("heading", { name: "Live" })).toBeVisible();
  await page.waitForTimeout(600);
  await expect(page.getByTestId("live-charge-graph")).toHaveCount(0);
  await expect(page.getByTestId("live-drive-map")).toHaveCount(0);
});

test("live page, live_v2 on while driving: the drive's map", async ({ page }) => {
  await stub(page, "driving", { designPreview: ["live_v2"] });
  await page.goto("/live");
  await expect(page.getByTestId("live-drive-map")).toBeVisible();
  await expect(page.getByTestId("live-charge-graph")).toHaveCount(0);
});

for (const [w, h] of [[393, 852], [1280, 900]] as const) {
  for (const theme of ["light", "dark"] as const) {
    for (const scenario of ["charging", "driving"] as const) {
      test(`shot live v2 ${scenario} ${w} ${theme}`, async ({ page }) => {
        await page.setViewportSize({ width: w, height: h });
        await page.emulateMedia({ colorScheme: theme });
        await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
        await stub(page, scenario, { designPreview: ["live_v2"] });
        await page.goto("/live");
        const target = page.getByTestId(scenario === "charging" ? "live-charge-graph" : "live-drive-map");
        await expect(target).toBeVisible();
        await page.waitForTimeout(1500);
        await target.scrollIntoViewIfNeeded();
        await page.screenshot({ path: path.join(SHOTS, `live-v2-${scenario}-${w}-${theme}.png`), fullPage: true });
      });
    }
  }
}
