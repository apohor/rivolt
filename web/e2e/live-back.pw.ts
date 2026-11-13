import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";

// Design 1 (design_preview "now_v2"): the Live screen has a way back to Now. Off, it is the Live page users have today.
const FULL_STATE = {
  ...JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8")),
  ...vehicleState("parked"),
};

for (const [flag, touch] of [[true, false], [true, true], [false, false]] as const) {
  test.describe(touch ? "touch" : "pointer", () => {
  test.use({ hasTouch: touch, isMobile: touch });
  test(`@webkit live page, now_v2 ${flag ? "on" : "off"}: ${flag ? "a back link returns to Now" : "no back link"}`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await stubApi(page, { nowEndpoint: true, designPreview: flag ? ["now_v2"] : [] });
    await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(FULL_STATE) }));
    await page.goto("/live");
    const back = page.getByTestId("live-back");
    if (!flag) {
      await expect(page.getByRole("heading", { name: "Live" })).toBeVisible();
      await expect(back).toHaveCount(0);
      return;
    }
    await expect(back).toBeVisible();
    // A finger needs 44 px, a mouse does not.
    expect((await back.boundingBox())!.height).toBeGreaterThanOrEqual(touch ? 44 : 30);
    await back.click();
    await expect(page).toHaveURL(/\/$/);
  });
  });
}
