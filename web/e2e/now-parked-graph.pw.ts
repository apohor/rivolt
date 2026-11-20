import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";
import { parkedSeriesFixture, type ParkedCase } from "./parkedSeriesFixture";

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
  await expect(page.getByTestId(wide ? "now-parked-row-wakes" : "now-parked-tile-wakes")).toContainText(/4/);
  await expect(page.getByTestId(wide ? "now-parked-row-idle" : "now-parked-tile-idle")).toContainText(/6% a day/);
});

// The header counts only the loss since the parked marker: the reading there to the ring's battery.
async function headerDelta(page: Page, parkedCase: ParkedCase) {
  const fix = parkedSeriesFixture(parkedCase);
  const cur = fix.points.filter((p) => p.seg === fix.points[fix.points.length - 1].seg);
  const g = page.getByTestId("now-parked-graph");
  const head = (await g.getByTestId("now-parked-change").textContent())!;
  const delta = parseFloat(head.replace("\u2212", "-"));
  return { head, delta, sinceParked: cur[cur.length - 1].soc - cur[0].soc, windowWide: fix.points[fix.points.length - 1].soc - fix.points[0].soc };
}

// The header is the difference of the graph's two labels: the start label sits on the parked marker's reading, the end on the ring's.
async function labels(page: Page) {
  const g = page.getByTestId("now-parked-graph");
  // The svg draws after the box is measured: wait for both labels.
  await expect(g.locator("svg text").filter({ hasText: /^\d+\.\d%$/ })).toHaveCount(2);
  const texts = await g.locator("svg text").allTextContents();
  const pcts = texts.filter((t) => /^\d+\.\d%$/.test(t)).map(parseFloat);
  const head = (await g.getByTestId("now-parked-change").textContent())!;
  return { start: pcts[0], end: pcts[pcts.length - 1], delta: parseFloat(head.replace("\u2212", "-")) };
}

for (const c of ["overnight", "charged"] as const) {
  test(`${c}: the header is the end label minus the start label, and the end is the ring's battery @webkit`, async ({ page }) => {
    await open(page, c);
    const { start, end, delta } = await labels(page);
    expect(delta).toBeCloseTo(end - start, 1);
    const fix = parkedSeriesFixture(c);
    const cur = fix.points.filter((p) => p.seg === 1);
    expect(start).toBeCloseTo(cur[0].soc, 1);
    await expect(page.getByRole("img", { name: new RegExp(`Battery ${Math.round(end)} percent`) })).toBeVisible();
  });
}

test("the header is the loss since the truck was parked, and says when @webkit", async ({ page }) => {
  await open(page, "overnight");
  const { head, delta, sinceParked } = await headerDelta(page, "overnight");
  expect(delta).toBeCloseTo(sinceParked, 0);
  const chip = (await page.getByTestId("now-pill").textContent())!.match(/since (\d+:\d+ [AP]M)/)![1];
  expect(head).toContain(`since ${chip}`);
});

test("a drive in the window does not count toward the header, the rate or the projection @webkit", async ({ page }) => {
  await open(page, "overnight");
  const { delta, sinceParked, windowWide } = await headerDelta(page, "overnight");
  // The drive cost 0.6 points and sits before the marker, so the two counts differ by about that.
  expect(Math.abs(delta - sinceParked)).toBeLessThan(0.15);
  expect(Math.abs(windowWide - sinceParked)).toBeGreaterThan(0.4);
  expect(Math.abs(delta - windowWide)).toBeGreaterThan(0.4);
});

test("a charge in the window does not turn the header into a gain @webkit", async ({ page }) => {
  await open(page, "charged");
  const { head, delta, sinceParked, windowWide } = await headerDelta(page, "charged");
  expect(windowWide).toBeGreaterThan(0 - 30); // the window as a whole is up or nearly flat across the charge
  expect(sinceParked).toBeLessThan(0);
  expect(delta).toBeCloseTo(sinceParked, 0);
  expect(head).toMatch(/^\u2212/);
  await expect(page.getByTestId("now-parked-graph").getByTestId("now-parked-projection")).toContainText(/At this rate/);
});

test("one parked time: the chip, the pill and the header say the same @webkit", async ({ page }) => {
  await open(page, "overnight");
  const chip = (await page.getByTestId("now-pill").textContent())!.match(/since (\d+:\d+ [AP]M)/)![1];
  const g = page.getByTestId("now-parked-graph");
  const pill = await g.locator("svg text", { hasText: /^Parked / }).allTextContents();
  for (const t of pill) expect(t).toBe(`Parked ${chip}`);
  await expect(g.getByTestId("now-parked-change")).toContainText(chip);
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
  // The chip says when the open sleep began; the header and the pill say when the truck was parked. Both are on the card and both are right.
  const chipText = (await page.getByTestId("now-pill").textContent())!;
  expect(chipText).toMatch(/^Asleep · since \d+:\d+ [AP]M/);
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
  const CASES: [ParkedCase, "parked" | "asleep"][] = [["overnight", "parked"], ["asleep", "asleep"], ["never_slept", "parked"], ["charged", "parked"], ["minutes", "parked"], ["gone", "parked"]];
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
      // The fixed header and tab bar sit over the card in a tall element shot; they are not part of it.
      await page.addStyleTag({ content: `${text === "200" ? "html{font-size:200%}" : ""} header, nav, .fixed, .sticky, [data-ui-header] { visibility: hidden !important }` });
      await page.waitForTimeout(500);
      const card = page.locator("[data-now-v2]").first();
      await card.screenshot({ path: `${SHOTS}/${c}-${w}-${scheme}-t${text}-${unit}.png` });
      await ctx.close();
    });
  }
}
