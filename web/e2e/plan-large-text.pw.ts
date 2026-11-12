import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Safari's aA text size zooms the page: at 200% a 390 px iPhone lays out 195 CSS px wide (300%: 130). The plan sheet must not scroll sideways there;
// the trip cost and the summary wrap instead of being clipped. At 300% (130 px) the sheet itself must fit; the app header, map overlays and
// tab bar at that width are a separate issue.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Las Vegas", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

for (const [zoom, width, height] of [[200, 195, 422], [300, 130, 281]] as const) {
  test(`@webkit phone at aA ${zoom}%: the plan sheet does not scroll sideways`, async ({ browser }, info) => {
    const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
    await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
    await page.goto("/trips/plan");
    await page.getByRole("tab", { name: "Saved" }).click();
    await page.getByRole("button", { name: /Old trip/ }).click();
    await page.getByRole("tab", { name: "Planner" }).click();
    await page.getByRole("button", { name: "Plan trip" }).click();
    await expect(page.locator("[data-plan-sheet]")).toBeVisible();
    // At peek and again with the sheet pulled to full, where the cost, the figures and the stops show.
    const handle = page.locator("[data-plan-sheet-handle]");
    for (const snap of ["peek", "full"] as const) {
      if (snap === "full") {
        await handle.click();
        await handle.click();
        await expect(page.locator("[data-plan-sheet]")).toHaveAttribute("data-snap", "full");
      }
      await page.waitForTimeout(500);
      const shot = info.outputPath(`plan-aa${zoom}-${snap}.png`);
      await page.screenshot({ path: shot });
      await info.attach(`plan-aa${zoom}-${snap}`, { path: shot, contentType: "image/png" });
      const wide = await page.evaluate((scope) => {
        const out: string[] = [];
        const W = document.documentElement.clientWidth;
        for (const el of Array.from(document.querySelectorAll<HTMLElement>(scope))) {
          const r = el.getBoundingClientRect();
          if (r.width > 0 && (r.right > W + 0.5 || r.left < -0.5)) out.push(`${el.tagName.toLowerCase()}.${String(el.className).slice(0, 60)} ${Math.round(r.left)}..${Math.round(r.right)} "${(el.textContent ?? "").slice(0, 30)}"`);
        }
        return { sw: document.documentElement.scrollWidth, W, out: out.slice(0, 15) };
      }, zoom === 200 ? "body *" : "[data-plan-sheet] *");
      expect(wide.out, `${snap}: sticks out of ${wide.W}px`).toEqual([]);
      if (zoom === 200) expect(wide.sw).toBeLessThanOrEqual(wide.W);
    }
    await ctx.close();
  });
}
