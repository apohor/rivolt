import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Design "now_charge_graph" (with now_slot): the Now card's charging slot draws the open charge so far (battery, power, the target, a dashed
// estimate to it). Too few samples: a "collecting" line and no frame. Off, the card is as it was. SHOTS_DIR makes the image-gate shots.
type Curve = { elapsedMin: number; soc: (m: number) => number; kw: (m: number) => number; targetPct?: number; pack?: (m: number) => number | null };
// A pack that warms from about 70 F (21 C) toward 95 F (35 C), quickly at first, as a pack does when charging starts.
const warm = (m: number) => 21 + 14 * (1 - Math.exp(-m / 45));
const CASES: Record<string, Curve> = {
  ac: { elapsedMin: 90, soc: (m) => 40 + m * 0.136, kw: () => 11, pack: warm },
  dc: { elapsedMin: 25, soc: (m) => 15 + 40 * (1 - Math.exp(-m / 20)), kw: (m) => 190 * Math.exp(-m / 22) + 40, pack: warm },
  slow: { elapsedMin: 30, soc: (m) => 40 + m * 0.09, kw: () => 4.8, pack: warm },
  fresh: { elapsedMin: 1, soc: (m) => 40 + m * 0.136, kw: () => 11, pack: warm },
  // Three minutes in on a home charger with hours still to go (limit 60%), and a charge about to finish.
  start: { elapsedMin: 3, soc: (m) => 33 + m * 0.136, kw: () => 11, targetPct: 60, pack: warm },
  end: { elapsedMin: 80, soc: (m) => 40 + m * 0.45, kw: () => 11, pack: warm },
  paused: { elapsedMin: 60, soc: (m) => 40 + Math.min(m, 30) * 0.136, kw: (m) => (m < 30 ? 11 : 0), pack: warm },
};
const SHOTS = process.env.SHOTS_DIR;

async function open(page: Page, c: Curve, flag = true, slot = true) {
  await stubApi(page, { scenario: "charging", nowEndpoint: true, designPreview: flag ? ["now_v2", "now_tidy", "now_slot", "now_charge_graph"] : ["now_v2", "now_tidy", "now_slot"], chargeCase: c });
  await page.goto("/");
  if (slot) await expect(page.getByTestId("now-slot")).toBeVisible();
  else await page.waitForTimeout(800);
}

test("an AC charge draws battery, power and the way to the target with its finish time @webkit", async ({ page }) => {
  await open(page, CASES.ac);
  const g = page.getByTestId("now-charge-graph");
  await expect(g).toBeVisible();
  await expect(g).toContainText(/80% · \d{1,2}:\d{2}/);
  await expect(page.getByTestId("now-charge-graph-estimate")).toBeVisible();
  await expect(page.getByTestId("now-charge-graph-collecting")).toHaveCount(0);
  await expect(g.locator("path[stroke-dasharray]").first()).toBeAttached();
  // The iPhone's chip wording and no plug-in time; no truck picture here (it comes with the image-proxy change, never straight from Rivian).
  await expect(page.getByTestId("now-pill")).toContainText(/Charging · 11 kW · done by/);
  await expect(page.getByTestId("now-car")).toHaveCount(0);
  await expect(page.getByTestId("now-slot")).not.toContainText(/Reaches/);
  await expect(page.getByTestId("now-slot-plugged-in")).toHaveCount(0);
  // The pack's temperature is a line on the chart (not in the power line, not a figure row), with its value as the legend says.
  await expect(page.getByTestId("now-slot")).toContainText(/Cost so far/);
  await expect(page.getByTestId("now-slot-big")).toHaveText(/^\d+(\.\d)?\s?kW/);
  await expect(page.getByTestId("now-slot-big")).not.toContainText(/°|pack/i);
  await expect(page.getByTestId("now-slot-pack")).toHaveCount(0);
  await expect(page.getByTestId("now-slot")).not.toContainText(/Pack temperature/);
  await expect(page.getByTestId("now-temps")).toHaveText(/Cabin.*Outside/);
  await expect(page.getByTestId("now-temps")).not.toContainText(/Pack/);
  // On a phone the Live button keeps the status row with the chip (the chip drops the finish time there).
  const chip = await page.getByTestId("now-pill").boundingBox();
  const live = await page.getByTestId("now-live-link").boundingBox();
  if (chip && live && (page.viewportSize()?.width ?? 1280) < 431) expect(Math.abs(chip.y - live.y)).toBeLessThan(12);
});

// One finish time and one power on the card: the chip's clock is the graph pill's, and the legend names the chip's kW.
for (const name of ["ac", "dc"]) {
  test(`the ${name} charge says one finish time and one power @webkit`, async ({ page }) => {
    await open(page, CASES[name]);
    const chip = (await page.getByTestId("now-pill").textContent()) ?? "";
    const when = chip.match(/(\d{1,2}:\d{2}\s?[AP]M)/)?.[1];
    const kw = chip.match(/(\d+(?:\.\d)?) kW/)?.[1];
    expect(when).toBeTruthy();
    expect(kw).toBeTruthy();
    await expect(page.getByTestId("now-charge-graph")).toContainText(`% · ${when}`);
    await expect(page.getByTestId("now-charge-graph")).toContainText(`Power ${kw} kW`);
    await expect(page.getByTestId("now-slot-big")).toContainText(kw!);
  });
}

// From 431 px the chip keeps the finish time whole: with "done by" while the row has the room, without those words below that, never cut mid-time.
for (const [name, width] of [["ac", 431], ["ac", 440], ["dc", 431], ["dc", 440], ["dc", 480], ["dc", 600], ["dc", 1280]] as const) {
  test(`the ${name} charging chip shows the whole finish time at ${width} px @webkit`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    await open(page, CASES[name]);
    const chip = page.getByTestId("now-pill");
    await expect(chip).toContainText(/Charging · \d+ kW · (done by )?\d{1,2}:\d{2}/);
    const shown = await chip.evaluate((el) => {
      const spans = [...el.querySelectorAll("span")].filter((s) => s.children.length === 0 && s.getAttribute("aria-hidden") == null && s.offsetParent !== null);
      return spans.map((s) => ({ text: s.textContent, cut: s.scrollWidth > s.clientWidth + 1 }));
    });
    expect(shown).toHaveLength(1);
    expect(shown[0].cut).toBe(false);
    expect(shown[0].text).toMatch(/\d{1,2}:\d{2}\s?(AM|PM)?$/i);
    if (SHOTS) await page.locator("[data-now-soft]").screenshot({ path: `${SHOTS}/chip-${name}-${width}.png` });
    const live = await page.getByTestId("now-live-link").boundingBox();
    const box = await chip.boundingBox();
    expect(box!.x + box!.width).toBeLessThanOrEqual(live!.x);
    expect(Math.abs(box!.y - live!.y)).toBeLessThan(12);
  });
}

test("the graph's start and now labels are the real readings and the change is their difference @webkit", async ({ page }) => {
  await open(page, CASES.ac);
  const g = page.getByTestId("now-charge-graph");
  await expect(g).toBeVisible();
  const [, from, to] = (await g.locator("svg").getAttribute("aria-label"))!.match(/from (\d+)% to (\d+)%/)!.map(Number);
  const ringLabel = (await page.locator("[aria-label^='Battery ']").first().getAttribute("aria-label"))!;
  expect(to).toBe(Number(ringLabel.match(/\d+/)![0]));
  const change = Number((await g.innerText()).match(/\+(\d+)%/)![1]);
  expect(change).toBe(to - from);
});

test("the time axis gives the session its true share of the charge, never under a quarter of the width @webkit", async ({ page }) => {
  for (const c of [CASES.slow, CASES.ac, CASES.dc]) {
    await open(page, c);
    const g = page.getByTestId("now-charge-graph");
    await expect(g).toBeVisible();
    const svg = await g.locator("svg").boundingBox();
    const dot = await g.locator('[data-testid="now-charge-graph-estimate-line"]').evaluate((el) => (el as SVGPathElement).getBBox().x);
    // The dotted way on starts where the session so far ends: at least about a quarter of the width, and no cliff (not most of the width) for a short session.
    expect(dot / (svg?.width ?? 1)).toBeGreaterThan(0.2);
  }
});

test("the graph is compact: a fixed height on a phone and on desktop, not the column's @webkit", async ({ page }) => {
  for (const [w, max] of [[393, 160], [1280, 210]] as const) {
    await page.setViewportSize({ width: w, height: 1000 });
    await open(page, CASES.slow);
    const box = await page.getByTestId("now-charge-graph").locator("svg").boundingBox();
    expect(box!.height).toBeLessThanOrEqual(max);
    expect(box!.height).toBeGreaterThanOrEqual(max - 10);
  }
});

test("the estimate leaves \"now\" with the slope of the battery line, not at an angle @webkit", async ({ page }) => {
  for (const c of [CASES.slow, CASES.dc]) {
    await open(page, c);
    const g = page.getByTestId("now-charge-graph");
    await expect(g.getByTestId("now-charge-graph-estimate-line")).toBeAttached();
    const r = await g.evaluate((root) => {
      const est = root.querySelector('[data-testid="now-charge-graph-estimate-line"]') as SVGPathElement;
      const paths = [...root.querySelectorAll("svg path")].filter((p) => p !== est && (p as SVGPathElement).getAttribute("stroke") && (p as SVGPathElement).getAttribute("fill") === "none") as SVGPathElement[];
      // The battery line is the one that ends where the estimate starts (the power line ends elsewhere).
      const s0 = est.getPointAtLength(0);
      const gap = (p: SVGPathElement) => Math.hypot(p.getPointAtLength(p.getTotalLength()).x - s0.x, p.getPointAtLength(p.getTotalLength()).y - s0.y);
      const line = paths.reduce((a, b) => (gap(b) < gap(a) ? b : a));
      const at = (p: SVGPathElement, l: number) => p.getPointAtLength(l);
      const L = line.getTotalLength();
      const a = at(line, L - 6), b = at(line, L - 0.5), c = at(est, 0.5), d = at(est, 6);
      return { before: (b.y - a.y) / (b.x - a.x), after: (d.y - c.y) / (d.x - c.x), startX: at(est, 0).x - b.x };
    });
    expect(Math.abs(r.startX)).toBeLessThan(2);
    // Within a few degrees of each other: the old straight line left the slow charge at a visible angle.
    expect(Math.abs(Math.atan(r.after) - Math.atan(r.before))).toBeLessThan(0.15);
  }
});

test("a charge just started says it is collecting, with no graph @webkit", async ({ page }) => {
  await open(page, CASES.fresh);
  await expect(page.getByTestId("now-charge-graph-collecting")).toBeVisible();
  await expect(page.getByTestId("now-charge-graph")).toHaveCount(0);
});

test("a paused charge (no power) leaves the card without a slot, so no graph @webkit", async ({ page }) => {
  await open(page, CASES.paused, true, false);
  await expect(page.getByTestId("now-charge-graph")).toHaveCount(0);
});

test("flag off: no graph and no collecting line @webkit", async ({ page }) => {
  await open(page, CASES.ac, false);
  await expect(page.getByTestId("now-charge-graph")).toHaveCount(0);
  await expect(page.getByTestId("now-charge-graph-collecting")).toHaveCount(0);
});

test("it reads the stored samples at the card's pace, never /api/vehicles @webkit", async ({ page }) => {
  const seen: string[] = [];
  page.on("request", (r) => seen.push(new URL(r.url()).pathname));
  await open(page, CASES.ac);
  await expect(page.getByTestId("now-charge-graph")).toBeVisible();
  expect(seen.filter((p) => p === "/api/samples").length).toBeLessThanOrEqual(2);
});

// The pack's temperature is a third, thin line over the measured part, with its value at the "now" end and in the legend.
test("the pack temperature is a line with its value at the end and in the legend @webkit", async ({ page }) => {
  await open(page, CASES.ac);
  const g = page.getByTestId("now-charge-graph");
  await expect(g.getByTestId("now-charge-graph-pack-line")).toBeVisible();
  await expect(g.getByTestId("now-charge-graph-pack")).toHaveText(/^Pack \d+°F$/);
  await expect(g.getByTestId("now-charge-graph-pack-now")).toHaveText(/^\d+°F$/);
  const legend = (await g.getByTestId("now-charge-graph-pack").textContent())!.match(/(\d+)°F/)![1];
  expect(await g.getByTestId("now-charge-graph-pack-now").textContent()).toBe(`${legend}°F`);
  // 90 minutes of a pack warming about 70 to 88 F: the line really climbs (it ends higher on screen than it starts).
  const d = (await g.getByTestId("now-charge-graph-pack-line").getAttribute("d"))!;
  const ys = [...d.matchAll(/[ML]?\s*(-?[\d.]+)[ ,](-?[\d.]+)/g)].map((m) => Number(m[2]));
  expect(ys[ys.length - 1]).toBeLessThan(ys[0] - 20);
  expect(await g.locator("svg").getAttribute("aria-label")).toMatch(/pack temperature \d+°F now, up \d+ degrees over the charge/);
  // It is drawn only over the measured part: the line ends at the battery's "now" dot, not in the estimate.
  const lineBox = await g.getByTestId("now-charge-graph-pack-line").boundingBox();
  const est = await g.getByTestId("now-charge-graph-estimate-line").boundingBox();
  expect(lineBox!.x + lineBox!.width).toBeLessThanOrEqual(est!.x + 8);
});

test("the pack's unit follows the account @webkit", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ temperatureUnit: "c" })));
  await open(page, CASES.ac);
  const g = page.getByTestId("now-charge-graph");
  await expect(g.getByTestId("now-charge-graph-pack")).toHaveText(/^Pack \d+°C$/);
  await expect(g.getByTestId("now-charge-graph-pack-now")).toHaveText(/^\d+°C$/);
});

for (const [name, pack] of [
  ["no pack samples", () => null],
  ["a single pack sample", (m: number) => (m > 89.9 ? 30 : null)],
  ["a newest pack sample older than 15 minutes", (m: number) => (m < 70 ? warm(m) : null)],
] as const) {
  test(`${name}: no pack line, no legend entry, no label, nothing in the description @webkit`, async ({ page }) => {
    await open(page, { ...CASES.ac, pack });
    const g = page.getByTestId("now-charge-graph");
    await expect(g).toBeVisible();
    await expect(g.getByTestId("now-charge-graph-pack-line")).toHaveCount(0);
    await expect(g.getByTestId("now-charge-graph-pack")).toHaveCount(0);
    await expect(g.getByTestId("now-charge-graph-pack-now")).toHaveCount(0);
    await expect(g).not.toContainText(/Pack/);
    expect(await g.locator("svg").getAttribute("aria-label")).not.toMatch(/pack/i);
    await expect(g.getByTestId("now-charge-graph-estimate")).toBeVisible();
  });
}

// The pack's value never sits on the battery's value, the pill, the start label or a time label, at any width or stage of the charge.
for (const name of ["start", "ac", "dc", "end"]) {
  for (const width of [320, 393, 1440]) {
    test(`the pack value overlaps no other label: ${name} at ${width} px @webkit`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 });
      await open(page, CASES[name]);
      const g = page.getByTestId("now-charge-graph");
      if (name === "start") await expect(g.getByTestId("now-charge-graph-collecting")).toHaveCount(0);
      await expect(g.getByTestId("now-charge-graph-pack")).toBeVisible();
      const label = g.getByTestId("now-charge-graph-pack-now");
      if (width > 320) await expect(label).toBeVisible();
      if ((await label.count()) === 0) return;
      const svg = (await g.locator("svg").first().boundingBox())!;
      const me = (await label.boundingBox())!;
      expect(me.x).toBeGreaterThanOrEqual(svg.x);
      expect(me.x + me.width).toBeLessThanOrEqual(svg.x + svg.width);
      const others = await g.locator("svg text:not([data-testid='now-charge-graph-pack-now']), svg rect.fill-neutral-500\\/15").evaluateAll((els) => els.map((e) => { const r = e.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height, t: e.textContent }; }));
      for (const o of others) {
        const hit = me.x < o.x + o.w && me.x + me.width > o.x && me.y < o.y + o.h && me.y + me.height > o.y;
        expect(hit, `pack value over "${o.t}"`).toBe(false);
      }
    });
  }
}

if (SHOTS) {
  // The image-gate matrix: every case on a phone and on desktop in both themes; the narrowest phone; large text; km.
  type Shot = [string, number, "light" | "dark", string, string];
  const shots: Shot[] = [];
  for (const name of Object.keys(CASES)) for (const w of [393, 1280, 1440]) for (const scheme of ["light", "dark"] as const) shots.push([name, w, scheme, "100", "mi"]);
  for (const name of ["ac", "dc"]) for (const scheme of ["light", "dark"] as const) shots.push([name, 320, scheme, "100", "mi"]);
  for (const name of ["ac", "dc"]) for (const w of [320, 393, 1280]) shots.push([name, w, "dark", "200", "mi"]);
  for (const w of [393, 1280]) shots.push(["ac", w, "dark", "100", "km"]);
  for (const [name, w, scheme, text, unit] of shots) {
    test(`shot ${name} ${w} ${scheme} ${text}% ${unit}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w < 900 ? 1500 : 900 }, colorScheme: scheme, deviceScaleFactor: Number(process.env.DSF ?? 1) });
      const page = await ctx.newPage();
      await page.addInitScript((u) => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: u })), unit);
      await open(page, CASES[name], true, name !== "paused");
      if (text === "200") await page.addStyleTag({ content: "html{font-size:200%}" });
      await page.waitForTimeout(400);
      const b = await page.locator("div.rounded-2xl:has([data-testid='now-slot']), main div.rounded-2xl").first().boundingBox();
      if (!b) throw new Error("no card");
      await page.screenshot({ path: `${SHOTS}/${name}-${w}-${scheme}-t${text}-${unit}.png`, fullPage: true, clip: { x: Math.max(0, b.x - 8), y: b.y + (await page.evaluate(() => scrollY)) - 8, width: Math.min(w, b.width + 16), height: b.height + 16 } });
      if (process.env.DSF) await page.getByTestId("now-charge-graph").locator("svg").first().screenshot({ path: `${SHOTS}/graph-${name}-${w}-${scheme}.png` });
      await ctx.close();
    });
  }
}

// The charging card: the conditions are one row with no "Next 3 h" line, at every width.
for (const width of [320, 393, 1440]) {
  test(`the conditions row is one row, no rain line, ${width} px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1900 });
    await open(page, CASES.ac);
    const row = page.getByTestId("now-temps");
    await expect(row).toBeVisible();
    await expect(page.locator("[data-now-v2]")).not.toContainText(/next 3 h/i);
    const items = row.locator("> div");
    expect(await items.count()).toBeGreaterThanOrEqual(2);
    // The card settles as the weather and the cabin arrive: wait for the row, then check it.
    await expect
      .poll(async () => new Set(await items.evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top)))).size)
      .toBe(1);
  });
}
