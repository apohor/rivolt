import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Activity Table and Map at 390 / 1280 / 1440, dark and light: nothing
// overlaps or overflows, table cells stay on one line, every text node is Geist at
// 11/12/14/24/36, and the key columns are visible on a phone.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280, 1440] as const) {
    for (const view of ["table", "map"] as const) {
      test(`activity ${view} · ${w}px · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: { width: w, height: w === 390 ? 844 : 900 }, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript(([t]) => {
          localStorage.setItem("rivolt.theme", t);
          localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
        }, [theme]);
        await stubApi(page);
        await page.goto(`/activity?view=${view}`);
        await expect(page.locator(`[data-activity-view='${view}']`)).toBeVisible();
        await page.waitForTimeout(1200);
        await page.screenshot({ path: path.join(SHOTS, `activity-${view}-${w}-${theme}.png`) });
        const res = await page.evaluate(() => {
          const out: Record<string, string[]> = { overflow: [], wrap: [], overlap: [], type: [] };
          if (document.documentElement.scrollWidth > window.innerWidth + 1) out.overflow.push(`page ${document.documentElement.scrollWidth} > ${window.innerWidth}`);
          document.querySelectorAll("[data-activity-table]").forEach((t) => {
            const wrap = t.closest(".overflow-x-auto") as HTMLElement | null;
            if (wrap && wrap.scrollWidth > wrap.clientWidth + 1 && window.innerWidth <= 640) out.overflow.push(`table scrolls ${wrap.scrollWidth} > ${wrap.clientWidth}`);
            t.querySelectorAll("td, th").forEach((c) => {
              if (!(c as HTMLElement).offsetParent) return;
              const cs = getComputedStyle(c);
              const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.5;
              const inner = (c as HTMLElement).clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
              if (inner > lh * 1.6) out.wrap.push(`${(c.textContent || "").trim().slice(0, 20)} (${Math.round(inner / lh)} lines)`);
            });
          });
          const ctrls = [...document.querySelectorAll("main button, main [role='tab'], main a")].filter((e) => (e as HTMLElement).offsetParent && !e.closest("tr, .leaflet-container, .maplibregl-map, [data-ui-feed]"));
          const rects = ctrls.map((e) => ({ n: (e.textContent || "").trim().slice(0, 16), r: e.getBoundingClientRect() }));
          for (let i = 0; i < rects.length; i++)
            for (let j = i + 1; j < rects.length; j++) {
              const a = rects[i].r, b = rects[j].r;
              if (a.width && b.width && a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1) out.overlap.push(`${rects[i].n} x ${rects[j].n}`);
            }
          const seen = new Set<Element>();
          const walker = document.createTreeWalker(document.querySelector("main")!, NodeFilter.SHOW_TEXT);
          for (let n = walker.nextNode(); n; n = walker.nextNode()) {
            if (!(n.textContent ?? "").trim()) continue;
            const el = n.parentElement!;
            if (seen.has(el) || (el as HTMLElement).offsetParent === null || el.closest(".leaflet-container, .maplibregl-map, svg")) continue;
            seen.add(el);
            const cs = getComputedStyle(el);
            const size = Math.round(parseFloat(cs.fontSize) * 10) / 10;
            const lab = `${el.tagName.toLowerCase()} "${(n.textContent ?? "").trim().slice(0, 18)}"`;
            if (!/Geist UI/.test(cs.fontFamily) || /mono/i.test(cs.fontFamily)) out.type.push(`family · ${lab}`);
            if (![11, 12, 14, 24, 36].includes(size)) out.type.push(`size ${size} · ${lab}`);
          }
          return out;
        });
        expect(res.overflow).toEqual([]);
        expect(res.wrap).toEqual([]);
        expect(res.overlap).toEqual([]);
        expect(res.type).toEqual([]);
        if (view === "table" && w === 390) {
          const t = page.locator("[data-activity-table]").first();
          for (const c of ["start", "distance", "soc", "cost"]) await expect(t.locator(`th[data-col='${c}']`)).toBeVisible();
        }
        await ctx.close();
      });
    }
  }
}
