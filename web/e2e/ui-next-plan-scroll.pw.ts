import { expect, test } from "@playwright/test";
import { settled } from "./settle";
import { stubApi } from "./stub";

// Phone, map-first: when a plan arrives the page brings the map into view by itself, so
// it fills the space between the top bar and the sheet's peek. No manual scroll.
for (const theme of ["dark", "light"] as const) {
  test(`phone: the map is in view above the peek right after planning · ${theme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: theme, reducedMotion: "reduce" });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => {
      localStorage.setItem("rivolt.theme", t);
    }, [theme]);
    await stubApi(page, { demo: true, signedIn: false });
    await page.goto("/demo/trips/plan");
    expect(await page.evaluate(() => window.scrollY)).toBe(0);
    await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
    await expect(page.locator("[data-plan-area='map'] .maplibregl-marker").first()).toBeVisible();
    // The page brings the map into view by itself; wait for the scroll and the map's fit to finish, not for a guessed time.
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
    await settled(page, ["[data-plan-area='map']", "[data-plan-sheet]"]);
    const res = await page.evaluate(() => {
      const sheet = document.querySelector("[data-plan-sheet]")!.getBoundingClientRect();
      const bar = document.querySelector("header.sticky")!.getBoundingClientRect();
      const map = document.querySelector("[data-plan-area='map']")!.getBoundingClientRect();
      const top = Math.max(map.top, bar.bottom);
      const visible = Math.max(0, Math.min(map.bottom, sheet.top) - top);
      const markers = [...document.querySelectorAll("[data-plan-area='map'] .maplibregl-marker")].map((m) => m.getBoundingClientRect());
      const hidden = markers.filter((m) => m.top < bar.bottom || m.bottom > sheet.top).length;
      return { scrollY: window.scrollY, visible, frac: visible / sheet.top, hidden, n: markers.length, focusInMap: !!document.activeElement?.closest("[data-plan-area='map']") };
    });
    expect(res.scrollY).toBeGreaterThan(0);
    expect(res.frac).toBeGreaterThanOrEqual(0.6);
    expect(res.n).toBeGreaterThan(1);
    expect(res.hidden).toBe(0);
    // The scroll doesn't steal focus into the map.
    expect(res.focusInMap).toBe(false);
    await page.screenshot({ path: `e2e/__screenshots__/plan-phone-scroll-${theme}.png` });
    await ctx.close();
  });
}

// #1984: the page above the map can change height while the smooth scroll to the map runs (here the heading goes, 120 ms in). The scroll was aimed
// once, so the map ended under the top bar by that much (at Safari's 300% text its Layers button was hidden there). Now the page is put right once
// the scroll comes to rest. On a busy phone (CI) the smooth scroll can also stop short of its aim; the second case cuts it short 200 ms in.
for (const cut of [false, true]) test(`@webkit phone: the map ends just under the top bar when the page above it changes during the scroll${cut ? ", and the scroll stops short" : ""}`, async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark", reducedMotion: "no-preference" });
  const page = await ctx.newPage();
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo/trips/plan");
  await expect(page.locator("main h1")).toBeVisible();
  await page.evaluate((cut) => {
    const h = document.querySelector("main h1") as HTMLElement;
    h.style.minHeight = "60px";
    new MutationObserver((_, mo) => {
      if (!document.querySelector("[data-plan-area='map']:not([data-empty])")) return;
      mo.disconnect();
      window.setTimeout(() => { h.style.display = "none"; }, 120);
      if (cut) window.setTimeout(() => window.scrollTo({ top: window.scrollY, behavior: "instant" }), 200);
    }).observe(document.body, { childList: true, subtree: true });
  }, cut);
  await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
  await expect(page.locator("main h1")).toBeHidden();
  await settled(page, ["[data-plan-area='map']", "[data-plan-sheet]"]);
  const gap = () => page.evaluate(() => Math.round(document.querySelector("[data-plan-area='map']")!.getBoundingClientRect().top - document.querySelector("header.sticky")!.getBoundingClientRect().bottom));
  await expect.poll(gap, { timeout: 3000 }).toBeGreaterThanOrEqual(6);
  expect(await gap()).toBeLessThanOrEqual(10);
  await ctx.close();
});
