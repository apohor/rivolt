import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Safari's aA text size zooms the page: at 200% a 390 px iPhone lays out 195 CSS px wide (300%: 130). The plan page must not scroll sideways there:
// the trip cost and the summary wrap instead of being clipped, and the app header, the map's charger chips and the tab bar fit too.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Las Vegas", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

for (const [zoom, width, height] of [[200, 195, 422], [300, 130, 281]] as const) {
  test(`@webkit phone at aA ${zoom}%: the plan sheet does not scroll sideways`, async ({ browser }, info) => {
    const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
    // Three routes, so the route choices show.
    await page.route("**/api/trips/plan", (r) => r.fulfill(j({ ...DEMO_PLAN, Routes: [DEMO_PLAN.Routes[0], DEMO_PLAN.Routes[0], DEMO_PLAN.Routes[0]] })));
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
      const wide = await page.evaluate(() => {
        const out: string[] = [];
        const W = document.documentElement.clientWidth;
        for (const el of Array.from(document.querySelectorAll<HTMLElement>("body *"))) {
          // The map clips its own markers and pointer tooltips; the scroll width check below still covers them. The route choices scroll sideways
          // on purpose: each must fit the scroller (checked below), not all of them the screen.
          if (el.closest(".maplibregl-map") || el.closest("[data-plan-routes]")) continue;
          const r = el.getBoundingClientRect();
          if (r.width > 0 && (r.right > W + 0.5 || r.left < -0.5)) out.push(`${el.tagName.toLowerCase()}.${String(el.className).slice(0, 60)} ${Math.round(r.left)}..${Math.round(r.right)} "${(el.textContent ?? "").slice(0, 30)}"`);
        }
        const routes = document.querySelector<HTMLElement>("[data-plan-routes]");
        if (routes)
          for (const card of Array.from(routes.querySelectorAll<HTMLElement>("[role=radio]")))
            if (card.offsetWidth > routes.clientWidth + 0.5 || card.scrollWidth > card.clientWidth + 0.5) out.push(`route choice ${card.offsetWidth}px (text ${card.scrollWidth}) in ${routes.clientWidth}px "${card.textContent?.slice(0, 20)}"`);
        return { sw: document.documentElement.scrollWidth, W, out: out.slice(0, 15) };
      });
      expect(wide.out, `${snap}: sticks out of ${wide.W}px`).toEqual([]);
      expect(wide.sw).toBeLessThanOrEqual(wide.W);
      if (snap === "full") {
        // The open sheet leaves the details most of its height to scroll in; the title over them must not take the room.
        const [body, sheet] = await page.evaluate(() => [
          document.querySelector("[data-plan-sheet-body]")!.clientHeight,
          document.querySelector("[data-plan-sheet]")!.clientHeight,
        ]);
        expect(body, `details ${body}px of a ${sheet}px sheet`).toBeGreaterThanOrEqual(sheet / 2);
      }
    }
    await ctx.close();
  });
}

// The narrow-layout breakpoints meet the wide ones with no width left between them: 1 px under 360 the peek lines clamp to two (360 and up
// truncate), 1 px under 200 the stop rows wrap and the trip cost shrinks together.
for (const width of [359, 199]) {
  test(`@webkit phone ${width} px wide: the narrow plan layout applies`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width, height: 422 }, colorScheme: "dark" });
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
    const got = await page.evaluate(() => {
      const h2 = getComputedStyle(document.querySelector("[data-plan-sheet-title] h2")!);
      const row = document.querySelector("[data-charger-type]")!.closest("button")!.firstElementChild!;
      return {
        clamp: h2.getPropertyValue("-webkit-line-clamp"),
        wrap: getComputedStyle(row).flexWrap,
        hero: getComputedStyle(document.querySelector("[data-plan-next]")!).getPropertyValue("--pt-hero").trim(),
      };
    });
    expect(got.clamp).toBe("2");
    if (width === 199) expect([got.wrap, got.hero]).toEqual(["wrap", "1.625rem"]);
    await ctx.close();
  });
}

