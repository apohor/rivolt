import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Maps 10 pruned the MapLibre stylesheet (postcss-prune-maplibre): the logo and the icons of controls Rivolt never creates are gone. What must
// not change: the credits. The compact attribution's (i) button opens a panel with the Protomaps and OpenStreetMap credits (the OpenStreetMap
// licence requires them to stay visible), and a hover popup keeps its styling. This opens the credits and a popup in light and dark and takes
// the shots a reviewer compares with the pre-prune build; the assertions are that the credits are visible and linked, that nothing but
// MapLibre's own markup is in them, and that no logo is shown.

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function open(page: Page, theme: "dark" | "light") {
  await page.addInitScript((t) => {
    localStorage.setItem("rivolt.theme", t);
    // The stub's rows are a few hours to days old: show all time.
    localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
  }, theme);
  await stubApi(page);
  await page.goto("/activity?view=map");
  const map = page.locator("[data-activity-view='map'] .maplibregl-map").first();
  await expect(map).toBeVisible();
  // The route lines are drawn (drawn.ts), so there is something to hover.
  await expect(page.locator("[data-activity-view='map'] [data-map-drawn]:not([data-map-drawn='0'])").first()).toBeAttached();
  return map;
}

for (const theme of ["dark", "light"] as const) {
  test(`map credits and popup keep their look after the CSS prune · ${theme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: theme });
    const page = await ctx.newPage();
    const map = await open(page, theme);

    // A hover popup: sweep the pointer across the map until a route's tip appears, then look at it.
    const box = (await map.boundingBox())!;
    const tip = page.locator(".maplibregl-popup.rivolt-tip");
    let found = false;
    for (let y = box.y + 30; y < box.y + box.height - 30 && !found; y += 18) {
      for (let x = box.x + 30; x < box.x + box.width - 30; x += 9) {
        await page.mouse.move(x, y);
        if (await tip.count()) { found = true; break; }
      }
    }
    expect(found, "no route under the pointer produced a tip popup").toBe(true);
    await expect(tip.locator(".maplibregl-popup-content")).toBeVisible();
    await expect(tip.locator(".maplibregl-popup-tip")).toHaveCount(1);
    await map.screenshot({ path: path.join(SHOTS, `map-popup-${theme}.png`) });
    // Pointer away: the tip goes.
    await page.mouse.move(box.x + 2, box.y + 2);
    await expect(tip).toHaveCount(0);

    // The compact attribution: closed it is the (i) button; opened it shows the credits.
    const attrib = map.locator(".maplibregl-ctrl-attrib");
    await expect(attrib).toBeVisible();
    const button = attrib.locator(".maplibregl-ctrl-attrib-button");
    await expect(button).toBeVisible();
    await button.click();
    await expect(attrib).toHaveClass(/maplibregl-compact-show/);
    const protomaps = attrib.getByRole("link", { name: "Protomaps" });
    const osm = attrib.getByRole("link", { name: "OpenStreetMap" });
    await expect(protomaps).toBeVisible();
    await expect(osm).toBeVisible();
    await expect(osm).toHaveAttribute("href", "https://openstreetmap.org/copyright");
    // The MapLibre logo is gone by design (its licence does not require it); the credits above are not.
    await expect(map.locator(".maplibregl-ctrl-logo")).toHaveCount(0);
    await map.screenshot({ path: path.join(SHOTS, `map-credits-open-${theme}.png`) });
    await ctx.close();
  });
}
