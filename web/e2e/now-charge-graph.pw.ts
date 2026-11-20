import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Design "now_charge_graph" (with now_slot): the Now card's charging slot draws the open charge so far (battery, power, the target, a dashed
// estimate to it). Too few samples: a "collecting" line and no frame. Off, the card is as it was. SHOTS_DIR makes the image-gate shots.
type Curve = { elapsedMin: number; soc: (m: number) => number; kw: (m: number) => number };
const CASES: Record<string, Curve> = {
  ac: { elapsedMin: 90, soc: (m) => 40 + m * 0.136, kw: () => 11 },
  dc: { elapsedMin: 25, soc: (m) => 15 + 40 * (1 - Math.exp(-m / 20)), kw: (m) => 190 * Math.exp(-m / 22) + 40 },
  fresh: { elapsedMin: 1, soc: (m) => 40 + m * 0.136, kw: () => 11 },
  paused: { elapsedMin: 60, soc: (m) => 40 + Math.min(m, 30) * 0.136, kw: (m) => (m < 30 ? 11 : 0) },
};
const SHOTS = process.env.SHOTS_DIR;

async function open(page: Page, c: Curve, flag = true, slot = true) {
  await stubApi(page, { scenario: "charging", nowEndpoint: true, designPreview: flag ? ["now_v2", "now_tidy", "now_slot", "now_charge_graph"] : ["now_v2", "now_tidy", "now_slot"], chargeCase: c });
  await page.goto("/");
  if (slot) await expect(page.getByTestId("now-slot")).toBeVisible();
  else await page.waitForTimeout(800);
}

test("an AC charge draws battery, power, the target and a dashed estimate", async ({ page }) => {
  await open(page, CASES.ac);
  const g = page.getByTestId("now-charge-graph");
  await expect(g).toBeVisible();
  await expect(g).toContainText("Target 80%");
  await expect(page.getByTestId("now-charge-graph-estimate")).toBeVisible();
  await expect(page.getByTestId("now-charge-graph-collecting")).toHaveCount(0);
  await expect(g.locator("path[stroke-dasharray]").first()).toBeAttached();
});

test("a charge just started says it is collecting, with no graph", async ({ page }) => {
  await open(page, CASES.fresh);
  await expect(page.getByTestId("now-charge-graph-collecting")).toBeVisible();
  await expect(page.getByTestId("now-charge-graph")).toHaveCount(0);
});

test("a paused charge (no power) leaves the card without a slot, so no graph", async ({ page }) => {
  await open(page, CASES.paused, true, false);
  await expect(page.getByTestId("now-charge-graph")).toHaveCount(0);
});

test("flag off: no graph and no collecting line", async ({ page }) => {
  await open(page, CASES.ac, false);
  await expect(page.getByTestId("now-charge-graph")).toHaveCount(0);
  await expect(page.getByTestId("now-charge-graph-collecting")).toHaveCount(0);
});

test("it reads the stored samples at the card's pace, never /api/vehicles", async ({ page }) => {
  const seen: string[] = [];
  page.on("request", (r) => seen.push(new URL(r.url()).pathname));
  await open(page, CASES.ac);
  await expect(page.getByTestId("now-charge-graph")).toBeVisible();
  expect(seen.filter((p) => p === "/api/samples").length).toBeLessThanOrEqual(2);
});

if (SHOTS) {
  for (const [name, c] of Object.entries(CASES)) {
    for (const w of [320, 393, 430, 1280]) {
      for (const scheme of ["light", "dark"] as const) {
        for (const text of ["100", "200"]) {
          for (const unit of name === "ac" ? ["mi", "km"] : ["mi"]) {
            test(`shot ${name} ${w} ${scheme} ${text}% ${unit}`, async ({ browser }) => {
              const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: scheme, deviceScaleFactor: 1 });
              const page = await ctx.newPage();
              await page.addInitScript((u) => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: u })), unit);
              await open(page, c, true, name !== "paused");
              if (text === "200") await page.addStyleTag({ content: "html{font-size:200%}" });
              await page.waitForTimeout(400);
              const b = await page.locator("div.rounded-2xl:has([data-testid='now-slot']), main div.rounded-2xl").first().boundingBox();
              if (!b) throw new Error("no card");
              await page.screenshot({ path: `${SHOTS}/${name}-${w}-${scheme}-t${text}-${unit}.png`, fullPage: true, clip: { x: Math.max(0, b.x - 8), y: b.y + (await page.evaluate(() => scrollY)) - 8, width: Math.min(w, b.width + 16), height: b.height + 16 } });
              await ctx.close();
            });
          }
        }
      }
    }
  }
}
