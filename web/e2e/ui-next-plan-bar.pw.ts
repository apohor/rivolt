import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Planner route bar (signed in): From/To rows with a swap, departure
// chips, the start SoC slider, and the rest under More options.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { n: "iphone", vp: { width: 390, height: 844 } },
    { n: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    test(`route bar · ${v.n} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page);
      await page.goto("/trips/plan");
      const bar = page.locator("[data-plan-bar]");
      await expect(bar.locator("[data-plan-field]")).toHaveCount(2);
      await expect(bar.getByRole("button", { name: "Now" })).toHaveAttribute("aria-pressed", "true");
      // Pick the Home preset for From, then swap: To takes it.
      await bar.locator("[data-plan-field][data-heading='From']").getByRole("button", { name: /Home/ }).click();
      await expect(bar.locator("[data-plan-field][data-heading='From']")).toContainText("Home");
      await bar.getByRole("button", { name: "Swap From and To" }).click();
      await expect(bar.locator("[data-plan-field][data-heading='To']")).toContainText("Home");
      await page.waitForTimeout(500);
      await page.screenshot({ path: path.join(SHOTS, `bar-${v.n}-${theme}-after.png`) });
      // More options: closed by default, drive mode lives inside.
      const toggle = bar.locator("[data-plan-more-toggle]");
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await expect(page.getByRole("radiogroup", { name: "Drive mode" })).toBeHidden();
      await toggle.click();
      await expect(page.getByRole("radiogroup", { name: "Drive mode" })).toBeVisible();
      await page.screenshot({ path: path.join(SHOTS, `bar-${v.n}-${theme}-more.png`) });
      const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container, .maplibregl-map").analyze();
      const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

// Owner screenshot (phone): option chips wrapped and overlapped the adapter toggle,
// labels wrapped to 2-3 lines, and a disabled number input duplicated the slider.
test("route bar options at 390px: stacked, no overlap, labels on one line, no duplicate SoC input", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.goto("/trips/plan");
  await page.locator("[data-plan-more-toggle]").click();
  const body = page.locator("[data-plan-more-body]");
  await expect(body).toBeVisible();
  // The adapter toggle (the old overlap victim) is present in this setup.
  await expect(body.getByRole("switch")).toHaveCount(1);
  await page.screenshot({ path: path.join(SHOTS, "bar-options-iphone-dark-after.png"), fullPage: true });
  // One starting-SoC control: the slider, no number input beside it.
  await expect(page.locator("[data-plan-bar] input[type='number'][placeholder]")).toHaveCount(0);
  await expect(page.getByLabel("Starting SoC")).toHaveCount(1);
  const res = await page.evaluate(() => {
    const body = document.querySelector("[data-plan-more-body]")!;
    const ctrls = [...body.querySelectorAll("input, select, button, [role='radio'], [role='switch']")].filter((e) => (e as HTMLElement).offsetParent);
    const rects = ctrls.map((e) => ({ n: `${e.tagName}:${(e.textContent || (e as HTMLInputElement).ariaLabel || "").trim().slice(0, 20)}`, r: e.getBoundingClientRect() }));
    const overlaps: string[] = [];
    for (let i = 0; i < rects.length; i++)
      for (let j = i + 1; j < rects.length; j++) {
        const a = rects[i].r, b = rects[j].r;
        if (a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1) overlaps.push(`${rects[i].n} x ${rects[j].n}`);
      }
    const labels = [...body.querySelectorAll("label > span:first-child, [role='radiogroup'] > span, [data-plan-settings] > div > span:first-child")].map((e) => {
      const cs = getComputedStyle(e);
      const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.5;
      return { t: (e.textContent || "").trim(), lines: Math.round(e.getBoundingClientRect().height / lh) };
    });
    const wide = rects.filter((x) => x.r.right > window.innerWidth + 1).map((x) => x.n);
    return { overlaps, labels, wide };
  });
  expect(res.overlaps).toEqual([]);
  expect(res.wide).toEqual([]);
  for (const l of res.labels) expect(l.lines, l.t).toBeLessThanOrEqual(1);
  await ctx.close();
});

// The owner's setup (desktop side panel, light, More options open, a via stop being
// added, adapter on): across widths and both themes, no controls overlap, no label
// wraps, nothing sticks out of the panel, and every text node is Geist on the scale.
const MATRIX = [
  { w: 390, h: 844 },
  { w: 768, h: 1024 },
  { w: 1280, h: 800 },
  { w: 1440, h: 900 },
] as const;
for (const theme of ["dark", "light"] as const) {
  for (const vp of MATRIX) {
    test(`route bar options matrix · ${vp.w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page);
      await page.goto("/trips/plan");
      await page.locator("[data-plan-more-toggle]").click();
      await page.getByRole("button", { name: /add via stop/ }).click();
      const adapter = page.locator("[data-plan-more-body]").getByRole("switch");
      await expect(adapter).toBeVisible();
      await adapter.click();
      await expect(adapter).toHaveAttribute("aria-checked", "true");
      await page.waitForTimeout(400);
      await page.screenshot({ path: path.join(SHOTS, `bar-matrix-${vp.w}-${theme}.png`), fullPage: true });
      const res = await page.evaluate(() => {
        const bar = document.querySelector("[data-plan-bar]")!;
        const body = bar.querySelector("[data-plan-more-body]")!;
        const panel = bar.getBoundingClientRect();
        const ctrls = [...bar.querySelectorAll("input, select, button, [role='radio'], [role='switch']")].filter((e) => (e as HTMLElement).offsetParent);
        const rects = ctrls.map((e) => ({ n: `${e.tagName}:${(e.textContent || (e as HTMLInputElement).ariaLabel || (e as HTMLInputElement).placeholder || "").trim().slice(0, 20)}`, r: e.getBoundingClientRect() }));
        const overlaps: string[] = [];
        for (let i = 0; i < rects.length; i++)
          for (let j = i + 1; j < rects.length; j++) {
            const a = rects[i].r, b = rects[j].r;
            if (a.width === 0 || b.width === 0) continue;
            if (a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1) overlaps.push(`${rects[i].n} x ${rects[j].n}`);
          }
        // The panel's content box (16px padding): chips and toggles must stay inside it.
        const outside = rects.filter((x) => x.r.right > panel.right - 16 + 1 || x.r.left < panel.left + 16 - 1).map((x) => x.n);
        const labels = [...body.querySelectorAll("label > span:first-child, [role='radiogroup'] > span, [data-plan-settings] > div > span:first-child")].map((e) => {
          const cs = getComputedStyle(e);
          const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.5;
          return { t: (e.textContent || "").trim(), lines: Math.round(e.getBoundingClientRect().height / lh) };
        });
        const type: string[] = [];
        const w = document.createTreeWalker(bar, NodeFilter.SHOW_TEXT);
        const seen = new Set<Element>();
        for (let n = w.nextNode(); n; n = w.nextNode()) {
          if (!(n.textContent ?? "").trim()) continue;
          const el = n.parentElement!;
          if (seen.has(el) || (el as HTMLElement).offsetParent === null) continue;
          seen.add(el);
          const cs = getComputedStyle(el);
          const size = Math.round(parseFloat(cs.fontSize) * 10) / 10;
          const label = `${el.tagName.toLowerCase()} "${(n.textContent ?? "").trim().slice(0, 20)}"`;
          if (!/Geist UI/.test(cs.fontFamily) || /mono/i.test(cs.fontFamily)) type.push(`family ${cs.fontFamily.slice(0, 20)} · ${label}`);
          if (![11, 12, 14, 24, 36].includes(size)) type.push(`size ${size} · ${label}`);
          if (![400, 500, 600].includes(Number(cs.fontWeight))) type.push(`weight ${cs.fontWeight} · ${label}`);
        }
        return { overlaps, outside, labels, type };
      });
      expect(res.overlaps).toEqual([]);
      expect(res.outside).toEqual([]);
      for (const l of res.labels) expect(l.lines, l.t).toBeLessThanOrEqual(1);
      expect(res.type).toEqual([]);
      await ctx.close();
    });
  }
}
