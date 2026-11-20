import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";
import type { ParkedCase } from "./parkedSeriesFixture";

// Design "now_parked_graph" (with now_tidy): the Now card of a parked, plugged or asleep truck draws the last 24 hours of the battery with its
// sleeps and wake-ups, three figures and the projection. Too little data, an older server, or the key off: the card is as it was.
// SHOTS_DIR makes the image-gate shots.
const SHOTS = process.env.SHOTS_DIR;
const KEYS = ["now_v2", "now_tidy", "now_slot", "now_parked_graph"];

async function open(page: Page, parkedCase: ParkedCase, scenario: "parked" | "asleep" = "parked", keys = KEYS) {
  await stubApi(page, { scenario, nowEndpoint: true, designPreview: keys, parkedCase });
  await page.goto("/");
  await expect(page.getByTestId("now-pill")).toBeVisible();
}

test("an overnight park draws the line, the sleeps, the wake-ups and the three figures @webkit", async ({ page }) => {
  await open(page, "overnight");
  const g = page.getByTestId("now-parked-graph");
  await expect(g).toBeVisible();
  await expect(g.getByTestId("now-parked-sleep")).toHaveCount(5);
  await expect(g).toContainText("Battery while parked");
  await expect(g.getByTestId("now-parked-change")).toHaveText(/^[−+]\d+\.\d%/);
  await expect(g.getByTestId("now-parked-projection")).toContainText(/At this rate: about \d+% by \w+ .*\(estimate\)\./);
  await expect(g).toContainText("24 h ago");
  await expect(g).toContainText("now");
  // The figures: one set of tiles on a phone, one set of rows from 900 px; never both visible.
  const wide = (page.viewportSize()?.width ?? 0) >= 900;
  await expect(page.getByTestId("now-parked-tiles")).toBeVisible({ visible: !wide });
  await expect(page.getByTestId("now-parked-rows")).toBeVisible({ visible: wide });
  await expect(page.getByTestId(wide ? "now-parked-row-wakes" : "now-parked-tile-wakes")).toContainText(/5/);
  await expect(page.getByTestId(wide ? "now-parked-row-idle" : "now-parked-tile-idle")).toContainText(/6% a day/);
});

test("the graph ends at the ring's battery, starts at the first reading, and the header is their difference @webkit", async ({ page }) => {
  await open(page, "overnight");
  const g = page.getByTestId("now-parked-graph");
  const label = (await g.locator("svg").getAttribute("aria-label"))!;
  const [, from, to] = label.match(/(\d+)% 24 hours ago to (\d+)% now/)!.map(Number);
  const texts = await g.locator("svg text").allTextContents();
  const pcts = texts.filter((t) => /^\d+\.\d%$/.test(t)).map(parseFloat);
  const [start, end] = [pcts[0], pcts[pcts.length - 1]];
  expect(Math.round(start)).toBe(from);
  expect(Math.round(end)).toBe(to);
  // The ring says the same battery as the end label.
  await expect(page.getByRole("img", { name: new RegExp(`Battery ${to} percent`) })).toBeVisible();
  const head = (await g.getByTestId("now-parked-change").textContent())!;
  const delta = parseFloat(head.replace("\u2212", "-"));
  expect(delta).toBeCloseTo(end - start, 1);
});

test("one parked time: the chip, the pill and nothing else says another @webkit", async ({ page }) => {
  await open(page, "overnight");
  const chip = (await page.getByTestId("now-pill").textContent())!.match(/since (\d+:\d+ [AP]M)/)![1];
  const g = page.getByTestId("now-parked-graph");
  const pill = await g.locator("svg text", { hasText: /^Parked / }).allTextContents();
  for (const t of pill) expect(t).toBe(`Parked ${chip}`);
  await expect(g.getByTestId("now-parked-change")).not.toContainText("since");
});

test("the line is one continuous path and the sleeps fill under it @webkit", async ({ page }) => {
  await open(page, "overnight");
  const g = page.getByTestId("now-parked-graph");
  await expect(g.locator('path[stroke-width="3"]')).toHaveCount(1);
  await expect(g.locator("path[clip-path]")).toHaveCount(5);
});

test("the bottom row has Outside as on the charging card @webkit", async ({ page }) => {
  await open(page, "overnight");
  await expect(page.getByTestId("now-temps")).toHaveText(/Cabin.*Outside|Outside.*Cabin/);
});

test("the three figures sit in one row on a phone at the default text size @webkit", async ({ page }) => {
  await open(page, "overnight");
  if ((page.viewportSize()?.width ?? 0) >= 900 || (page.viewportSize()?.width ?? 0) < 360) return;
  const tops = await page.getByTestId("now-parked-tiles").locator("> div").evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top)));
  expect(new Set(tops).size).toBe(1);
});

test("an asleep truck shows an open sleep that ends at now @webkit", async ({ page }) => {
  await open(page, "asleep", "asleep");
  const g = page.getByTestId("now-parked-graph");
  await expect(g).toBeVisible();
  await expect(g.getByTestId("now-parked-sleep")).toHaveCount(5);
  // "Asleep today" is in the figures, not repeated in the conditions row.
  await expect(page.getByTestId("now-cond-asleep")).toHaveCount(0);
});

test("a truck that never slept has no sleep spans, no wake-up dots and a faster rate @webkit", async ({ page }) => {
  await open(page, "never_slept");
  const g = page.getByTestId("now-parked-graph");
  await expect(g).toBeVisible();
  await expect(g.getByTestId("now-parked-sleep")).toHaveCount(0);
  await expect(g).not.toContainText("woke up");
  await expect(page.getByTestId("now-parked-tile-wakes")).toHaveCount(0);
});

test("parked a few minutes (no readings yet) leaves the card as it was @webkit", async ({ page }) => {
  await open(page, "minutes");
  await expect(page.getByTestId("now-parked-graph")).toHaveCount(0);
  await expect(page.locator("[data-now-soft]")).toHaveCount(0);
});

test("an older server without the endpoint leaves the card as it was @webkit", async ({ page }) => {
  await open(page, "gone");
  await expect(page.getByTestId("now-parked-graph")).toHaveCount(0);
});

test("key off: no graph and no request @webkit", async ({ page }) => {
  const seen: string[] = [];
  page.on("request", (r) => seen.push(new URL(r.url()).pathname));
  await open(page, "overnight", "parked", KEYS.filter((k) => k !== "now_parked_graph"));
  await expect(page.getByTestId("now-parked-graph")).toHaveCount(0);
  expect(seen.filter((p) => p.endsWith("/parked-series"))).toHaveLength(0);
});

if (SHOTS) {
  // The image-gate matrix: widths 320/393/430/1280/1440, both themes, text 100% and 200%, every state, miles and kilometres.
  type Shot = [ParkedCase, "parked" | "asleep", number, "light" | "dark", string, string];
  const shots: Shot[] = [];
  const CASES: [ParkedCase, "parked" | "asleep"][] = [["overnight", "parked"], ["asleep", "asleep"], ["never_slept", "parked"], ["minutes", "parked"], ["gone", "parked"]];
  for (const [c, sc] of CASES) for (const w of [393, 1280]) for (const scheme of ["light", "dark"] as const) shots.push([c, sc, w, scheme, "100", "mi"]);
  for (const w of [320, 430, 1440]) for (const scheme of ["light", "dark"] as const) shots.push(["overnight", "parked", w, scheme, "100", "mi"]);
  for (const w of [320, 393, 1280]) shots.push(["overnight", "parked", w, "dark", "200", "mi"]);
  for (const w of [393, 1280]) shots.push(["overnight", "parked", w, "dark", "100", "km"]);
  for (const [c, sc, w, scheme, text, unit] of shots) {
    test(`shot ${c} ${w} ${scheme} ${text}% ${unit}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w < 900 ? 1700 : 1000 }, colorScheme: scheme, deviceScaleFactor: 1 });
      const page = await ctx.newPage();
      await page.addInitScript((u) => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: u })), unit);
      await open(page, c, sc);
      if (text === "200") await page.addStyleTag({ content: "html{font-size:200%}" });
      await page.waitForTimeout(500);
      const card = page.locator("[data-now-v2]").first();
      await card.screenshot({ path: `${SHOTS}/${c}-${w}-${scheme}-t${text}-${unit}.png` });
      await ctx.close();
    });
  }
}
