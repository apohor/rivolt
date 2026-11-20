import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The Now card's placeholder is as tall as the card, so what is under it does not jump down when the truck's state arrives.
for (const [name, keys] of [["redesigned", ["now_v2", "now_tidy", "now_slot"]], ["plain", undefined]] as const) {
  for (const width of [390, 768, 1024, 1280]) {
    test(`the Now placeholder matches the card's height (${name}, ${width})`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await stubApi(page, { nowEndpoint: true, ...(keys ? { designPreview: [...keys] } : {}) });
      await page.route(/\/api\/state\//, async (route) => {
        await new Promise((r) => setTimeout(r, 4000));
        await route.fallback();
      });
      await page.goto("/");
      const skeleton = page.getByTestId("now-skeleton");
      await expect(skeleton).toBeVisible();
      // The placeholder is swapped once when the vehicle list arrives (one for the other): read it until it holds still.
      let before = 0;
      await expect(async () => {
        before = (await skeleton.boundingBox())!.height;
      }).toPass();
      await expect(skeleton).toHaveCount(0, { timeout: 10_000 });
      // The card's own box, or its children's when the grid lays them out directly.
      const after = await page.getByRole("region", { name: "Now" }).evaluate((el) => {
        if (getComputedStyle(el).display !== "contents") return el.getBoundingClientRect().height;
        const r = [...el.children].map((c) => c.getBoundingClientRect());
        return Math.max(...r.map((x) => x.bottom)) - Math.min(...r.map((x) => x.top));
      });
      expect(Math.abs(after - before), `placeholder ${before} px, card ${after} px`).toBeLessThanOrEqual(24);
    });
  }
}

// A charging card with its graph is taller than a parked one: the placeholder takes the height the card had the last time.
for (const width of [393, 1440]) {
  test(`the placeholder takes the last height of a charging card (${width})`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await stubApi(page, {
      scenario: "charging",
      nowEndpoint: true,
      designPreview: ["now_v2", "now_tidy", "now_slot", "now_charge_graph"],
      chargeCase: { elapsedMin: 90, soc: (m) => 40 + m * 0.136, kw: () => 11 },
    });
    const card = () =>
      page.getByRole("region", { name: "Now" }).evaluate((el) => {
        if (getComputedStyle(el).display !== "contents") return el.getBoundingClientRect().height;
        const r = [...el.children].map((c) => c.getBoundingClientRect());
        return Math.max(...r.map((x) => x.bottom)) - Math.min(...r.map((x) => x.top));
      });
    await page.goto("/");
    await expect(page.getByTestId("now-charge-graph")).toBeVisible();
    // The height is stored once the card stops growing.
    await expect.poll(() => page.evaluate(() => window.localStorage.getItem("rivolt.nowCardHeights"))).not.toBeNull();
    await page.waitForTimeout(500);
    const drawn = await card();
    await page.route(/\/api\/state\//, async (route) => {
      await new Promise((r) => setTimeout(r, 4000));
      await route.fallback();
    });
    await page.reload();
    const skeleton = page.getByTestId("now-skeleton");
    let before = 0;
    await expect(async () => {
      before = (await skeleton.boundingBox())!.height;
    }).toPass();
    expect(Math.abs(before - drawn), `placeholder ${before} px, card ${drawn} px`).toBeLessThanOrEqual(24);
  });
}

test("sign-out forgets the remembered heights", async ({ page }) => {
  await stubApi(page, { scenario: "charging", nowEndpoint: true, designPreview: ["now_v2"] });
  await page.goto("/");
  await expect.poll(() => page.evaluate(() => window.localStorage.getItem("rivolt.nowCardHeights"))).not.toBeNull();
  await page.goto("/settings");
  await page.getByRole("button", { name: "Sign out of Rivolt", exact: true }).click();
  await page.waitForURL(/\/login/);
  expect(await page.evaluate(() => window.localStorage.getItem("rivolt.nowCardHeights"))).toBeNull();
});
