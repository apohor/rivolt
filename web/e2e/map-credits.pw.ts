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

    // The compact attribution (foldCredits): folded to its (i) until tapped, then the credits show, and a second tap folds them again.
    const attrib = map.locator(".maplibregl-ctrl-attrib");
    await expect(attrib).toBeVisible();
    const button = attrib.locator(".maplibregl-ctrl-attrib-button");
    await expect(button).toBeVisible();
    const protomaps = attrib.getByRole("link", { name: "Protomaps" });
    const osm = attrib.getByRole("link", { name: "OpenStreetMap" });
    await expect(protomaps).toBeHidden();
    await expect(osm).toBeHidden();
    await button.click();
    await expect(protomaps).toBeVisible();
    await expect(osm).toBeVisible();
    await expect(osm).toHaveAttribute("href", "https://openstreetmap.org/copyright");
    // The MapLibre logo is gone by design (its licence does not require it); the credits above are not.
    await expect(map.locator(".maplibregl-ctrl-logo")).toHaveCount(0);
    await map.screenshot({ path: path.join(SHOTS, `map-credits-open-${theme}.png`) });
    await button.click();
    await expect(protomaps).toBeHidden();
    await expect(osm).toBeHidden();

    // Keyboard focus rings the (i)'s box and the zoom buttons in the accent colour, never the browser default.
    await page.mouse.move(box.x + 2, box.y + 2);
    for (let i = 0; i < 40; i++) {
      await page.keyboard.press("Tab");
      if (await button.evaluate((el) => el === document.activeElement)) break;
    }
    await expect(button).toBeFocused();
    const ring = await attrib.evaluate((el) => {
      const c = getComputedStyle(el);
      return { style: c.outlineStyle, width: c.outlineWidth, color: c.outlineColor };
    });
    expect(ring.style).toBe("solid");
    expect(ring.width).toBe("2px");
    expect(await button.evaluate((el) => getComputedStyle(el).outlineStyle)).toBe("none");
    expect(await button.evaluate((el) => getComputedStyle(el).boxShadow)).toBe("none");
    await map.screenshot({ path: path.join(SHOTS, `map-focus-ring-${theme}.png`) });
    const zoomIn = map.locator(".maplibregl-ctrl-zoom-in");
    await page.keyboard.press("Shift+Tab");
    await page.keyboard.press("Shift+Tab");
    await zoomIn.focus();
    await page.keyboard.press("Shift+Tab");
    await page.keyboard.press("Tab");
    await expect(zoomIn).toBeFocused();
    // The ring is a 2 px box inside the button (its ::after), drawn above the icon and the neighbouring button.
    expect(await zoomIn.evaluate((el) => { const c = getComputedStyle(el, "::after"); return `${c.borderBottomStyle} ${c.borderBottomWidth}`; })).toBe("solid 2px");
    // The ring is closed on all four sides: the button below it does not cover its bottom edge (the point just inside the bottom edge hits
    // the focused button itself, not the next one).
    for (const zoom of [zoomIn, map.locator(".maplibregl-ctrl-zoom-out")]) {
      await zoom.focus();
      await page.keyboard.press("Shift+Tab");
      await page.keyboard.press("Tab");
      await expect(zoom).toBeFocused();
      const hit = await zoom.evaluate((el) => {
        const r = el.getBoundingClientRect();
        // The point may land on the icon inside the button: the button must contain what is hit, not a neighbour.
        const at = (y: number) => document.elementFromPoint(r.left + r.width / 2, y);
        return !!at(r.bottom - 1) && el.contains(at(r.bottom - 1)) && !!at(r.top + 1) && el.contains(at(r.top + 1));
      });
      expect(hit, "a neighbouring button covers the focused one's top or bottom edge").toBe(true);
      expect(await zoom.evaluate((el) => getComputedStyle(el).zIndex)).toBe("1");
      // What is on screen, not only what is hit: the row of pixels just inside the bottom edge (and the top one) is the accent colour across the
      // button's width (the corners are rounded, so the outer 4 px are left out).
      const accent = await zoom.evaluate((el) => getComputedStyle(el, "::after").borderBottomColor);
      const m = accent.match(/\d+/g)!.map(Number);
      const box = (await zoom.boundingBox())!;
      // The second pixel row of the 2 px band on each side (a button on a half pixel blurs the outermost row).
      for (const [edge, y] of [["bottom", box.y + box.height - 2], ["top", box.y + 1]] as const) {
        const png = await page.screenshot({ clip: { x: box.x, y, width: box.width, height: 1 } });
        const share = await page.evaluate(async ([b64, rgb]) => {
          const img = new Image();
          img.src = `data:image/png;base64,${b64}`;
          await img.decode();
          const c = document.createElement("canvas");
          c.width = img.width;
          c.height = img.height;
          const ctx = c.getContext("2d")!;
          ctx.drawImage(img, 0, 0);
          const edge = Math.round(4 * (img.width / (img.naturalWidth || img.width)));
          const px = ctx.getImageData(edge, 0, img.width - 2 * edge, 1).data;
          let hit = 0;
          const n = px.length / 4;
          for (let i = 0; i < n; i++) if (Math.abs(px[i * 4] - rgb[0]) < 40 && Math.abs(px[i * 4 + 1] - rgb[1]) < 40 && Math.abs(px[i * 4 + 2] - rgb[2]) < 40) hit++;
          return hit / n;
        }, [png.toString("base64"), m] as const);
        expect(share, `the ${edge} edge of the focused zoom button is drawn in the accent colour`).toBeGreaterThan(0.8);
      }
    }
    expect(await zoomIn.evaluate((el) => getComputedStyle(el).boxShadow)).toBe("none");
    await map.screenshot({ path: path.join(SHOTS, `map-focus-ring-zoom-${theme}.png`) });
    await ctx.close();
  });
}
