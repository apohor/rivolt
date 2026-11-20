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

// A card with its graph is taller than one without: the placeholder takes the height the card had the last time, from the first paint (the vehicle is
// not known yet: the one picked last is), and the card keeps it while its graph's series is on its way. Sampled in the page every 25 ms from the
// reload on, with the vehicles, the state and the series each answering late: the placeholder is never far from the card, and the section under
// it (the month) does not move once it is drawn.
const GRAPH_KEYS = ["now_v2", "now_tidy", "now_slot", "now_charge_graph", "now_parked_graph"];
const kinds = [
  { name: "charging", graph: "now-charge-graph", opts: { scenario: "charging", chargeCase: { elapsedMin: 90, soc: (m: number) => 40 + m * 0.136, kw: () => 11 } } },
  { name: "parked", graph: "now-parked-graph", opts: { scenario: "parked", parkedCase: "overnight" } },
] as const;
for (const kind of kinds) {
  for (const width of [393, 1280]) {
    test(`the page does not jump while a ${kind.name} card's data arrives (${width})`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await stubApi(page, { ...kind.opts, nowEndpoint: true, designPreview: GRAPH_KEYS });
      await page.goto("/");
      await expect(page.getByTestId(kind.graph)).toBeVisible();
      // The height is stored once the card stops growing.
      await expect.poll(() => page.evaluate(() => window.localStorage.getItem("rivolt.nowCardHeights"))).not.toBeNull();
      await page.waitForTimeout(800);
      const drawn = await page.getByRole("region", { name: "Now" }).evaluate((el) => {
        if (getComputedStyle(el).display !== "contents") return el.getBoundingClientRect().height;
        const r = [...el.children].map((c) => c.getBoundingClientRect());
        return Math.max(...r.map((x) => x.bottom)) - Math.min(...r.map((x) => x.top));
      });

      // The sleep warning is a banner between the card and the month that comes when its own data does (by design): not what is measured here.
      await page.route(/\/api\/sleep-activity/, (route) => route.fulfill({ status: 200, contentType: "application/json", body: "[]" }));
      const late = (re: RegExp, ms: number) =>
        page.route(re, async (route) => {
          await new Promise((r) => setTimeout(r, ms));
          await route.fallback();
        });
      await late(/\/api\/vehicles$/, 500);
      await late(/\/api\/state\//, 1200);
      await late(/\/api\/(charges$|samples|vehicles\/[^/]+\/parked-series)/, 1600);
      await page.addInitScript(() => {
        const w = window as unknown as { __samples: { top: number | null; sk: number | null }[] };
        w.__samples = [];
        setInterval(() => {
          const month = document.querySelector('[data-testid="now-month"]');
          const sk = document.querySelector('[data-testid="now-skeleton"]');
          w.__samples.push({
            top: month ? Math.round(month.getBoundingClientRect().top + window.scrollY) : null,
            sk: sk ? Math.round(sk.getBoundingClientRect().height) : null,
          });
        }, 25);
      });
      await page.reload();
      await expect(page.getByTestId(kind.graph)).toBeVisible({ timeout: 10_000 });
      await page.waitForTimeout(800);
      const samples = await page.evaluate(() => (window as unknown as { __samples: { top: number | null; sk: number | null }[] }).__samples);

      const sk = samples.map((x) => x.sk).filter((h): h is number => h != null);
      expect(sk.length, "the placeholder was on screen").toBeGreaterThan(3);
      expect(Math.max(...sk.map((h) => Math.abs(h - drawn))), `placeholder heights ${[...new Set(sk)]} px, card ${drawn} px`).toBeLessThanOrEqual(24);
      const tops = samples.map((x) => x.top).filter((t): t is number => t != null);
      expect(tops.length, "the month section was drawn").toBeGreaterThan(3);
      expect(Math.max(...tops) - Math.min(...tops), `the month section's top moved through ${[...new Set(tops)]}`).toBeLessThanOrEqual(8);
    });
  }
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
