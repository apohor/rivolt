import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";

// The sample truck's vehicle record carries configurator image URLs, but the demo draws its own truck and
// never asks rivian.com for anything.
test("demo Now shows the drawn truck and makes no request to rivian.com", async ({ page }) => {
  const asked: string[] = [];
  page.on("request", (r) => {
    if (/(^|\.)rivian\.com$/.test(new URL(r.url()).hostname)) asked.push(r.url());
  });
  await stubApi(page, { demo: true, signedIn: false, designPreview: ["now_v2"] });
  const images = ["three-quarter", "side-charging"].flatMap((placement) =>
    ["dark", "light"].map((design) => ({ vehicle_id: VEHICLE_ID, url: `https://rivian.com/mobile/static/img/${placement}-${design}.png`, placement, design, size: "large", resolution: "xxhdpi" })),
  );
  await page.route("**/api/vehicles", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: VEHICLE_ID, vin: "SAMPLE0000000001", name: "Sample R1S", model: "R1S", pack_kwh: 135, images }]) }),
  );
  await page.route("**/rivian.com/**", (r) => r.abort());
  await page.goto("/demo");
  await expect(page.getByTestId("now-car-sketch")).toBeVisible();
  await expect(page.getByTestId("now-car")).toHaveCount(0);
  expect(asked).toEqual([]);
});
