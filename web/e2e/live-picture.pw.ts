import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";

const FULL = JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8"));

// The Live page's vehicle card: while the truck drives or charges the panel takes the whole width and the picture is neither drawn nor requested;
// parked, asleep or offline it stays (it shows the open doors, frunk and tailgate). SHOTS_DIR makes the before/after shots.
const SHOTS = process.env.SHOTS_DIR;
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64");

async function open(page: Page, scenario: "parked" | "driving" | "charging" | "asleep") {
  await stubApi(page, { scenario });
  const requests: string[] = [];
  await page.route("**/stub-truck.png", (r) => {
    requests.push(r.request().url());
    return r.fulfill({ status: 200, contentType: "image/png", body: PNG });
  });
  await page.route("**/api/vehicles", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: VEHICLE_ID, vin: "SAMPLE0000000001", name: "Sample R1S", model: "R1S", pack_kwh: 135, image_url: "/stub-truck.png" }]) }));
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...FULL, ...vehicleState(scenario) }) }));
  await page.goto("/live");
  await expect(page.getByTestId("live-vehicle-row")).toBeVisible();
  return requests;
}

for (const [scenario, pictured] of [["driving", false], ["charging", false], ["parked", true], ["asleep", true]] as const) {
  test(`@webkit live card, ${scenario}: the picture is ${pictured ? "there" : "absent and never requested"}`, async ({ page }) => {
    await page.setViewportSize({ width: 393, height: 900 });
    const requests = await open(page, scenario);
    const img = page.getByTestId("live-vehicle-row").locator("img");
    if (pictured) {
      await expect(img).toHaveCount(1);
      expect(requests.length).toBeGreaterThan(0);
    } else {
      await page.waitForTimeout(500);
      await expect(img).toHaveCount(0);
      expect(requests).toEqual([]);
    }
  });
}

test("@webkit live card, driving at 1440: the panel takes the freed width", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, "driving");
  const row = (await page.getByTestId("live-vehicle-row").boundingBox())!;
  const panel = (await page.getByTestId("live-vehicle-row").locator("> div").first().boundingBox())!;
  expect(panel.width).toBeGreaterThan(row.width * 0.95);
});

if (SHOTS) {
  for (const scenario of ["driving", "charging", "parked"] as const) for (const w of [393, 1440]) {
    test(`shot ${scenario} ${w}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 1400 }, colorScheme: "dark" });
      const page = await ctx.newPage();
      await open(page, scenario);
      await page.waitForTimeout(800);
      await page.screenshot({ path: `${SHOTS}/live-${scenario}-${w}.png` });
      await ctx.close();
    });
  }
}
