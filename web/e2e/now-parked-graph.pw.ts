import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";
import { parkedSeriesFixture, type ParkedCase } from "./parkedSeriesFixture";

// Design "now_parked_graph" (with now_tidy): the Now card of a parked, plugged or asleep truck draws the last 24 hours of the battery with its
// sleeps and wake-ups, three figures and the projection. Too little data, an older server, or the key off: the card is as it was.
// SHOTS_DIR makes the image-gate shots.
const SHOTS = process.env.SHOTS_DIR;
const KEYS = ["now_v2", "now_tidy", "now_slot", "now_parked_graph"];

async function open(page: Page, parkedCase: ParkedCase, scenario: "parked" | "asleep" = "parked", keys = KEYS, parkedDelayMs = 0, parkedPlace: "home" | "away" | "unknown" = "home") {
  await stubApi(page, { scenario, nowEndpoint: true, designPreview: keys, parkedCase, parkedDelayMs, parkedPlace });
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
  const g = page.getByTestId("now-parked-graph");
  // The chip first shows the live read's parked time and takes the series' once that is read: the header comes from the series, so wait for the chip to say what the header says.
  await expect(g.getByTestId("now-parked-change")).toContainText(/since \d/);
  const chip = (await g.getByTestId("now-parked-change").textContent())!.match(/since (\d+:\d+ [AP]M)/)![1];
  await expect(page.getByTestId("now-pill")).toContainText(`since ${chip}`);
  const pill = await g.locator("svg text", { hasText: /^Parked / }).allTextContents();
  for (const t of pill) expect(t).toBe(`Parked ${chip}`);
  await expect(g.getByTestId("now-parked-change")).toContainText(chip);
});

test("the chip shows no parked time until the series answers, then the header's @webkit", async ({ page }) => {
  await open(page, "overnight", "parked", KEYS, 1200);
  const pill = page.getByTestId("now-pill");
  // While the series is out, the chip names the state without a time: it never shows one that the header then contradicts.
  await expect(pill).toHaveText(/^Parked$/);
  const g = page.getByTestId("now-parked-graph");
  await expect(g.getByTestId("now-parked-change")).toContainText(/since \d/);
  const head = (await g.getByTestId("now-parked-change").textContent())!.match(/since (\d+:\d+ [AP]M)/)![1];
  await expect(pill).toContainText(`since ${head}`);
});

test("a series that takes too long leaves the chip with the state's own time @webkit", async ({ page }) => {
  await open(page, "overnight", "parked", KEYS, 6000);
  await expect(page.getByTestId("now-pill")).toHaveText(/^Parked$/);
  await expect(page.getByTestId("now-pill")).toContainText(/Parked · since \d+:\d+ [AP]M/, { timeout: 5000 });
  await expect(page.getByTestId("now-parked-graph")).toHaveCount(0);
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

test("an asleep truck's chip takes the start of the open sleep from the series @webkit", async ({ page }) => {
  await open(page, "asleep", "asleep");
  await expect(page.getByTestId("now-parked-graph")).toBeVisible();
  // The fixture's open sleep began 2.6 h before the e2e clock; that is the chip's time, not the card's own run.
  const want = await page.evaluate((t) => new Date(t).toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" }).replace(/\s/g, " "), e2eNow() - 2.6 * 3_600_000);
  await expect(page.getByTestId("now-pill")).toContainText(`Asleep · since ${want}`);
});

test("with no loss the header says so, the wake-up tile has no cost and the two tiles fill the row, and the start value is not repeated @webkit", async ({ page }) => {
  await open(page, "flat", "asleep");
  const g = page.getByTestId("now-parked-graph");
  await expect(g.getByTestId("now-parked-change")).toHaveText(/^No loss\s*since \d+:\d+ [AP]M/);
  await expect(g).not.toContainText("0.0%");
  const width = page.viewportSize()?.width ?? 0;
  if (width < 900) {
    const tiles = page.getByTestId("now-parked-tiles").locator("> div");
    await expect(tiles).toHaveCount(2);
    await expect(page.getByTestId("now-parked-tile-wakes")).not.toContainText("each");
    if (width >= 360) {
      const box = await page.getByTestId("now-parked-tiles").boundingBox();
      const last = await tiles.last().boundingBox();
      expect(Math.round(last!.x + last!.width)).toBeGreaterThanOrEqual(Math.round(box!.x + box!.width) - 1);
    }
  }
  // One value label at the end of the flat tail, not two.
  const label = g.locator("text", { hasText: /^\d+\.\d%$/ });
  await expect(label).toHaveCount(1);
});

test("parked a few minutes after a drive: the pill ends at the card edge and the value sits clear of the line @webkit", async ({ page }) => {
  await open(page, "just", "parked");
  const g = page.getByTestId("now-parked-graph");
  await expect(g).toBeVisible();
  const box = await g.locator("svg").boundingBox();
  const pill = g.locator("text", { hasText: /^Parked \d+:\d+ [AP]M$/ });
  if ((page.viewportSize()?.width ?? 0) >= 900 || (await pill.count()) > 0) {
    const pb = await pill.boundingBox();
    expect(pb!.x + pb!.width).toBeLessThanOrEqual(box!.x + box!.width);
    expect(pb!.x).toBeGreaterThanOrEqual(box!.x);
  }
  // The current value is below the dot, so it never sits on the line that dipped just before it.
  const labelY = Number(await g.locator("text", { hasText: /^\d+\.\d%$/ }).last().getAttribute("y"));
  const dotY = Number(await g.locator("circle").last().getAttribute("cy"));
  expect(labelY).toBeGreaterThan(dotY);
});

test("a sleep before the parked marker draws its fill at 0.65, as on iPhone (the group's and the fill's opacity multiply) @webkit", async ({ page }) => {
  await open(page, "just", "parked");
  const sleeps = page.getByTestId("now-parked-sleep");
  await expect(sleeps.first()).toBeVisible();
  const shown = await sleeps.first().evaluate((g) => Number(g.getAttribute("opacity") ?? 1) * Number(g.querySelector("path")?.getAttribute("fill-opacity") ?? 1));
  expect(shown).toBeCloseTo(0.65, 2);
});

test("the last stored reading is 90 minutes old: the axis, the line's end dot and the open sleep run to now @webkit", async ({ page }) => {
  await open(page, "stale", "asleep");
  const g = page.getByTestId("now-parked-graph");
  await expect(g).toBeVisible();
  const box = (await g.locator("svg").boundingBox())!;
  const edge = box.x + box.width - 10; // PAD.r
  // The end dot (the last circle) is at the right edge, at the ring's value, not 90 minutes short of it.
  const dot = (await g.locator("circle").last().boundingBox())!;
  expect(dot.x + dot.width / 2).toBeGreaterThan(edge - 1.5);
  // The open sleep's bar ends there too.
  const bar = (await g.getByTestId("now-parked-sleep").last().locator("rect").last().boundingBox())!;
  expect(bar.x + bar.width).toBeGreaterThan(edge - 3);
  // The chip, the header and the tiles agree: the chip is the open sleep's start, the header runs from the parked marker, the tiles are the server's.
  const want = await page.evaluate((t) => new Date(t).toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" }).replace(/\s/g, " "), e2eNow() - 2.6 * 3_600_000);
  await expect(page.getByTestId("now-pill")).toContainText(`Asleep · since ${want}`);
  await expect(g.getByTestId("now-parked-change")).toContainText(/since \d+:\d+ [AP]M/);
  if ((page.viewportSize()?.width ?? 0) < 900) await expect(page.getByTestId("now-parked-tile-asleep")).toBeVisible();
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

// #2326: at 320 px and 200% text the hero wraps; the chip keeps its words and the range keeps its label inside the card.
test("the hero wraps at 320 px and 200% text: the chip's words and the range label stay inside the card", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 320, height: 1700 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, "overnight");
  await page.addStyleTag({ content: "html{font-size:200%}" });
  // The parked chip shows its time once the series has answered.
  await expect(page.getByTestId("now-pill")).toContainText("since");
  const clipped = await page.evaluate(() => {
    const card = document.querySelector("[data-now-v2]")!.getBoundingClientRect();
    const chip = document.querySelector("[data-now-chip]")!;
    const range = [...document.querySelectorAll("[data-now-v2] span")].find((e) => e.textContent === "range")!;
    const inside = (e: Element) => e.getBoundingClientRect().right <= card.right + 0.5;
    const cut = [...chip.querySelectorAll("span")].some((e) => e.scrollWidth > e.clientWidth + 1);
    return { chipCut: cut, chipIn: inside(chip), rangeIn: inside(range), chipText: chip.textContent };
  });
  expect(clipped.chipCut).toBe(false);
  expect(clipped.chipIn).toBe(true);
  expect(clipped.rangeIn).toBe(true);
  expect(clipped.chipText).toContain("since");
  await ctx.close();
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
      const ctx = await browser.newContext({ viewport: { width: w, height: w < 900 ? 1700 : 1000 }, colorScheme: scheme, deviceScaleFactor: Number(process.env.DSF ?? 1) });
      const page = await ctx.newPage();
      await page.addInitScript((u) => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: u })), unit);
      await open(page, c, sc);
      // The fixed header and tab bar sit over the card in a tall element shot; they are not part of it.
      await page.addStyleTag({ content: `${text === "200" ? "html{font-size:200%}" : ""} header, nav, .fixed, .sticky, [data-ui-header] { visibility: hidden !important }` });
      await page.waitForTimeout(500);
      const card = page.locator("[data-now-v2]").first();
      await card.screenshot({ path: `${SHOTS}/${c}-${w}-${scheme}-t${text}-${unit}.png` });
      if (process.env.DSF) await page.getByTestId("now-parked-graph").locator("svg").first().screenshot({ path: `${SHOTS}/graph-${c}-${w}-${scheme}.png` });
      await ctx.close();
    });
  }
}

// The graph is the card's content: no map, one line saying where the truck is (the saved place, else how far from home), nothing when it is not known.
for (const [place, want] of [["home", /^At Home$/], ["away", /^Away · [\d.]+ (mi|km) from home$/]] as const) {
  test(`the parked card has no map and says ${place === "home" ? "At Home" : "how far from home"} @webkit`, async ({ page }) => {
    await open(page, "overnight", "asleep", KEYS, 0, place);
    await expect(page.getByTestId("now-parked-graph")).toBeVisible();
    await expect(page.getByTestId("now-slot-map")).toHaveCount(0);
    const line = page.getByTestId("now-parked-place");
    await expect(line).toHaveText(want);
    // The card around it opens Live: the line is not a control of its own.
    await expect(page.getByRole("link", { name: "Full telemetry" })).toBeVisible();
  });
}

test("a truck whose place is not known has no map and no empty row @webkit", async ({ page }) => {
  await open(page, "overnight", "asleep", KEYS, 0, "unknown");
  await expect(page.getByTestId("now-parked-graph")).toBeVisible();
  await expect(page.getByTestId("now-slot-map")).toHaveCount(0);
  await expect(page.getByTestId("now-parked-place")).toHaveCount(0);
  await expect(page.getByTestId("now-slot")).toHaveCount(0);
});

test("without the key the parked card keeps its map and has no place line @webkit", async ({ page }) => {
  await open(page, "overnight", "asleep", ["now_v2", "now_tidy", "now_slot"]);
  await expect(page.getByTestId("now-slot")).toBeVisible();
  await expect(page.getByTestId("now-parked-place")).toHaveCount(0);
  await expect(page.getByTestId("now-parked-graph")).toHaveCount(0);
});

// The weather row is Cabin, Outside and Wind in one row, with no "Next 3 h" and nothing alone on a second row, at every width.
for (const width of [320, 393, 1440]) {
  test(`the conditions row is one row, no rain line, ${width} px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1900 });
    await open(page, "overnight");
    const row = page.getByTestId("now-temps");
    await expect(row).toBeVisible();
    await expect(row).not.toContainText("Next 3 h");
    await expect(page.locator("[data-now-v2]")).not.toContainText(/next 3 h/i);
    const items = row.locator("> div");
    expect(await items.count()).toBeGreaterThanOrEqual(2);
    // The card settles as the weather and the cabin arrive: wait for the row, then check it.
    await expect
      .poll(async () => new Set(await items.evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top)))).size)
      .toBe(1);
    const box = (await row.boundingBox())!;
    const right = await items.evaluateAll((els) => Math.max(...els.map((e) => e.getBoundingClientRect().right)));
    expect(right).toBeLessThanOrEqual(box.x + box.width + 1);
  });
}
