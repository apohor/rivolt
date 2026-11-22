import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";

// The Live screen has a way back to Now.
const FULL_STATE = {
  ...JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8")),
  ...vehicleState("parked"),
};

for (const touch of [false, true]) {
  test.describe(touch ? "touch" : "pointer", () => {
  test.use({ hasTouch: touch, isMobile: touch });
  test(`@webkit live page: a back link returns to Now`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await stubApi(page, { nowEndpoint: true });
    await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(FULL_STATE) }));
    await page.goto("/live");
    const back = page.getByTestId("live-back");
    await expect(back).toBeVisible();
    // A finger needs 44 px, a mouse does not.
    expect((await back.boundingBox())!.height).toBeGreaterThanOrEqual(touch ? 44 : 30);
    await back.click();
    await expect(page).toHaveURL(/\/$/);
  });
  });
}
