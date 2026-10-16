import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// ui_theme_next planner route bar (signed in): From/To rows with a swap, departure
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
        localStorage.setItem("rivolt.ui.next", "on");
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
      const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
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
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
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
