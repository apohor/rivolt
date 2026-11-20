import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Design "now_charge_graph" (with now_slot): the Now card's charging slot draws the open charge so far (battery, power, the target, a dashed
// estimate to it). Too few samples: a "collecting" line and no frame. Off, the card is as it was. SHOTS_DIR makes the image-gate shots.
type Curve = { elapsedMin: number; soc: (m: number) => number; kw: (m: number) => number };
const CASES: Record<string, Curve> = {
  ac: { elapsedMin: 90, soc: (m) => 40 + m * 0.136, kw: () => 11 },
  dc: { elapsedMin: 25, soc: (m) => 15 + 40 * (1 - Math.exp(-m / 20)), kw: (m) => 190 * Math.exp(-m / 22) + 40 },
  slow: { elapsedMin: 30, soc: (m) => 40 + m * 0.09, kw: () => 4.8 },
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
  // The pack's temperature (from the stored samples) is a figure row right after Rate; the bottom row is cabin and outside only.
  await expect(page.getByTestId("now-slot")).toContainText(/Cost so far/);
  await expect(page.getByTestId("now-slot")).toContainText(/Rate.*Pack temperature.*Elapsed/);
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
    await expect(page.getByTestId("now-charge-graph")).toContainText(`Power ${kw} kW now`);
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

test("a slow home charge with hours left still gives the session so far most of the width @webkit", async ({ page }) => {
  await open(page, CASES.slow);
  const g = page.getByTestId("now-charge-graph");
  await expect(g).toBeVisible();
  const svg = await g.locator("svg").boundingBox();
  const dot = await g.locator('[data-testid="now-charge-graph-estimate-line"]').evaluate((el) => (el as SVGPathElement).getBBox().x);
  // The dotted way on starts past half the width: the data so far is not squeezed into a stub.
  expect(dot / (svg?.width ?? 1)).toBeGreaterThan(0.5);
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

if (SHOTS) {
  // The image-gate matrix: every case on a phone and on desktop in both themes; the narrowest phone; large text; km.
  type Shot = [string, number, "light" | "dark", string, string];
  const shots: Shot[] = [];
  for (const name of Object.keys(CASES)) for (const w of [393, 1280]) for (const scheme of ["light", "dark"] as const) shots.push([name, w, scheme, "100", "mi"]);
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
