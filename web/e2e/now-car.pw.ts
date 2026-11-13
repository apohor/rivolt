import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";

// The Now hero shows the vehicle's Rivian image: three-quarter when parked, side-charging while charging, the design
// that matches the theme. Without images, or when one fails to load, the sketch of the model shows. Image URLs here are made up and served by the test.
const names = ["front", "in-use", "overhead", "rear", "side-charging", "side-rear", "side", "three-quarter"];
const images = names.flatMap((placement) =>
  ["dark", "light"].map((design) => ({ vehicle_id: VEHICLE_ID, url: `https://img.example/${placement}-${design}.png`, placement, design, size: "large", resolution: "xxhdpi" })),
);
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64");

async function open(page: Page, o: { scenario?: "parked" | "charging"; theme?: "dark" | "light"; withImages?: boolean; model?: string }) {
  await stubApi(page, { scenario: o.scenario ?? "parked", designPreview: ["now_v2"] });
  await page.route("**/img.example/**", (r) => r.fulfill({ status: 200, contentType: "image/png", body: PNG }));
  await page.route("**/api/vehicles", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: VEHICLE_ID, vin: "SAMPLE0000000001", name: "Sample R1S", model: o.model ?? "R1S", pack_kwh: 135, images: o.withImages === false ? [] : images }]) }),
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

test("no images: no broken picture, the sketch of the model shows (R1T, R1S, anything else is R1S), in both themes", async ({ page }) => {
  await open(page, { withImages: false });
  await expect(page.getByTestId("now-car")).toHaveCount(0);
  const sketch = page.getByTestId("now-car-sketch");
  await expect(sketch).toHaveAttribute("data-model", "R1S");
  await expect(sketch.getByRole("img", { name: "Rivian R1S" })).toBeVisible();
  for (const [model, want] of [["R1T", "R1T"], ["R2", "R1S"]] as const) {
    await open(page, { withImages: false, model });
    await expect(page.getByTestId("now-car-sketch")).toHaveAttribute("data-model", want);
  }
  // The body colour comes from --car-body and differs between the themes.
  const body = async () => page.getByTestId("now-car-sketch").locator("path[fill*='car-body']").first().evaluate((e) => getComputedStyle(e).fill);
  const dark = await body();
  await open(page, { withImages: false, theme: "light" });
  expect(await body()).not.toBe(dark);
});

test("an image that 404s gives way to the sketch on Now, and is dropped on Live; a good one shows on both", async ({ page }) => {
  const FULL = { ...JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8")), ...vehicleState("parked") };
  await open(page, {});
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(FULL) }));
  await page.goto("/live");
  await expect(page.getByRole("img", { name: "Sample R1S" })).toHaveCount(1);
  await page.route("**/img.example/**", (r) => r.fulfill({ status: 404, body: "" }));
  await page.reload();
  await expect(page.getByRole("img", { name: "Sample R1S" })).toHaveCount(0);
  await page.goto("/");
  await expect(page.getByTestId("now-car")).toHaveCount(0);
  await expect(page.getByTestId("now-car-sketch")).toBeVisible();
});

test("the Live pill has its own row: the image starts at least 8 px below it", async ({ page }) => {
  for (const [w, theme] of [[393, "dark"], [393, "light"], [1280, "dark"]] as const) {
    await page.setViewportSize({ width: w, height: 900 });
    await open(page, { theme });
    const pill = (await page.getByTestId("now-live-link").boundingBox())!;
    const car = (await page.getByTestId("now-car").boundingBox())!;
    expect(car.y).toBeGreaterThanOrEqual(pill.y + pill.height + 8);
  }
});

test("desktop: the hero takes the full content width and the Live button is compact", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 800 });
  await open(page, {});
  const hero = page.locator("[data-now-v2]");
  const main = page.getByTestId("now-month");
  const [h, m] = [await hero.boundingBox(), await main.boundingBox()];
  expect(Math.abs(h!.width - m!.width)).toBeLessThan(4);
  const live = await page.getByTestId("now-live-link").boundingBox();
  expect(live!.height).toBeLessThanOrEqual(34);
});

test("touch: the Live button keeps its 44 px target", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, hasTouch: true, isMobile: true });
  const page = await ctx.newPage();
  await open(page, {});
  const live = await page.getByTestId("now-live-link").boundingBox();
  expect(live!.height).toBeGreaterThanOrEqual(44);
  await ctx.close();
});

test("charging: the bolt on the sketch stays small", async ({ page }) => {
  await open(page, { scenario: "charging", withImages: false });
  const sketch = await page.getByTestId("now-car-sketch").boundingBox();
  const bolt = await page.locator("[data-testid=now-car-sketch] > svg").nth(1).boundingBox();
  expect(bolt!.width).toBeLessThan(sketch!.width / 4);
});

test("desktop parked: the temps open the right column with no rule above them; on a phone the rule stays", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 800 });
  await open(page, { scenario: "parked" });
  const temps = page.getByTestId("now-temps");
  await expect(page.getByTestId("now-subline")).toHaveCount(0);
  await expect(temps).toHaveCSS("border-top-width", "0px");
  await page.setViewportSize({ width: 393, height: 852 });
  await expect(temps).toHaveCSS("border-top-width", "1px");
});
