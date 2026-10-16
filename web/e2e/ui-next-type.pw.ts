import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// ui_theme_next type scale on Now, Truck, Money and Activity: Geist only, sizes
// from 11/12/14/24/36 (inputs 16 so iOS doesn't zoom), weights 400-600.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const PAGES = [
  { name: "now", url: "/", scenario: "charging", ready: "Now" },
  { name: "truck", url: "/truck", scenario: "parked", ready: "Truck" },
  { name: "money", url: "/money", scenario: "parked", ready: "Money" },
  { name: "activity", url: "/activity", scenario: "parked", ready: "Activity" },
] as const;

for (const theme of ["dark", "light"] as const) {
  for (const pg of PAGES) {
    test(`type scale · ${pg.name} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { scenario: pg.scenario, drain: "ok" });
      await page.goto(pg.url);
      await page.waitForTimeout(1800);
      await page.screenshot({ path: path.join(SHOTS, `type-${pg.name}-${theme}-after.png`), fullPage: true });
      const bad = await page.evaluate(() => {
        const out: string[] = [];
        const root = document.querySelector("main")!;
        const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
        const seen = new Set<Element>();
        for (let n = w.nextNode(); n; n = w.nextNode()) {
          if (!(n.textContent ?? "").trim()) continue;
          const el = n.parentElement!;
          if (seen.has(el) || el.closest(".leaflet-container, svg, script, style")) continue;
          seen.add(el);
          const cs = getComputedStyle(el);
          const size = Math.round(parseFloat(cs.fontSize) * 10) / 10;
          const label = `${el.tagName.toLowerCase()}.${String(el.className).slice(0, 40)} "${(n.textContent ?? "").trim().slice(0, 24)}"`;
          if (!/Geist UI/.test(cs.fontFamily) || /mono/i.test(cs.fontFamily)) out.push(`family ${cs.fontFamily.slice(0, 24)} · ${label}`);
          if (![11, 12, 14, 24, 36].includes(size)) out.push(`size ${size} · ${label}`);
          if (![400, 500, 600].includes(Number(cs.fontWeight))) out.push(`weight ${cs.fontWeight} · ${label}`);
        }
        return out;
      });
      expect(bad).toEqual([]);
      await ctx.close();
    });
  }
}
