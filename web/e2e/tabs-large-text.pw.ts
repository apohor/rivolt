import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Safari's aA text size zooms the page: at 200% a 390 px iPhone lays out 195 CSS px wide (300%: 130). The tabs other than Plan must not scroll
// sideways there either: whatever does not fit wraps, shrinks or goes, and only a row made to scroll sideways (overflow-x auto) may be wider.
const PAGES = ["/", "/activity", "/money", "/truck"];

for (const [zoom, width, height] of [[200, 195, 422], [300, 130, 281]] as const) {
  for (const path of PAGES) {
    test(`@webkit phone at aA ${zoom}%: ${path} does not scroll sideways`, async ({ browser }, info) => {
      const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: "dark" });
      const page = await ctx.newPage();
      await stubApi(page);
      await page.goto(path);
      await page.locator("main").first().waitFor();
      await page.waitForLoadState("networkidle");
      await page.waitForTimeout(500);
      const shot = info.outputPath(`tab${path.replace(/\//g, "-") || "-now"}-aa${zoom}.png`);
      await page.screenshot({ path: shot, fullPage: true });
      await info.attach(`tab ${path} aa${zoom}`, { path: shot, contentType: "image/png" });
      const wide = await page.evaluate(() => {
        const out: string[] = [];
        const W = document.documentElement.clientWidth;
        const scrolls = (el: Element) => {
          for (let p = el.parentElement; p; p = p.parentElement) {
            const o = getComputedStyle(p).overflowX;
            if (o === "auto" || o === "scroll" || o === "hidden" || o === "clip") return p.getBoundingClientRect().right <= W + 0.5 && p.getBoundingClientRect().left >= -0.5;
          }
          return false;
        };
        for (const el of Array.from(document.querySelectorAll<HTMLElement>("body *"))) {
          if (el.closest(".maplibregl-map")) continue;
          const r = el.getBoundingClientRect();
          if (r.width > 0 && (r.right > W + 0.5 || r.left < -0.5) && !scrolls(el))
            out.push(`${el.tagName.toLowerCase()}.${String(el.className).slice(0, 60)} ${Math.round(r.left)}..${Math.round(r.right)} "${(el.textContent ?? "").slice(0, 30)}"`);
        }
        return { sw: document.documentElement.scrollWidth, W, out: out.slice(0, 80) };
      });
      expect(wide.out, `${path}: sticks out of ${wide.W}px`).toEqual([]);
      expect(wide.sw).toBeLessThanOrEqual(wide.W);
      await ctx.close();
    });
  }
}

// The narrow-width rules put the tabs' grids in one column; a calendar keeps its seven days a row (data-keep-grid).
test("@webkit phone at aA 300%: the departure calendar keeps its weeks", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 130, height: 281 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.goto("/trips/plan");
  await page.getByRole("button", { name: /Select date/ }).click();
  const days = page.locator("[data-keep-grid]");
  await expect(days).toBeVisible();
  expect(await days.evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(" ").length)).toBe(7);
  await ctx.close();
});
