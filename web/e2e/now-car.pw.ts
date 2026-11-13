import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";

// The Now hero shows the vehicle's Rivian image: three-quarter when parked, side-charging while charging, the design
// that matches the theme. Without images the drawn truck stays. Image URLs here are made up and served by the test.
const names = ["front", "in-use", "overhead", "rear", "side-charging", "side-rear", "side", "three-quarter"];
const images = names.flatMap((placement) =>
  ["dark", "light"].map((design) => ({ vehicle_id: VEHICLE_ID, url: `https://img.example/${placement}-${design}.png`, placement, design, size: "large", resolution: "xxhdpi" })),
);
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64");

async function open(page: Page, o: { scenario?: "parked" | "charging"; theme?: "dark" | "light"; withImages?: boolean }) {
  await stubApi(page, { scenario: o.scenario ?? "parked", designPreview: ["now_v2"] });
  await page.route("**/img.example/**", (r) => r.fulfill({ status: 200, contentType: "image/png", body: PNG }));
  await page.route("**/api/vehicles", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: VEHICLE_ID, vin: "SAMPLE0000000001", name: "Sample R1S", model: "R1S", pack_kwh: 135, images: o.withImages === false ? [] : images }]) }),
  );
  await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), o.theme ?? "dark");
  await page.goto("/");
}

test("@webkit parked: the three-quarter image in the dark design, beside the gauge", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await open(page, {});
  const car = page.getByTestId("now-car");
  await expect(car).toHaveAttribute("src", "https://img.example/three-quarter-dark.png");
  await expect(page.locator("[data-now-v2] svg[viewBox='0 0 240 104']")).toHaveCount(0);
});

test("light theme picks the light design", async ({ page }) => {
  await open(page, { theme: "light" });
  await expect(page.getByTestId("now-car")).toHaveAttribute("src", "https://img.example/three-quarter-light.png");
});

test("charging shows the side-charging image", async ({ page }) => {
  await open(page, { scenario: "charging" });
  await expect(page.getByTestId("now-car")).toHaveAttribute("src", "https://img.example/side-charging-dark.png");
});

test("no images: no broken picture, the drawn truck stays", async ({ page }) => {
  await open(page, { withImages: false });
  await expect(page.getByTestId("now-car")).toHaveCount(0);
  await expect(page.locator("[data-now-v2] svg").first()).toBeVisible();
});
