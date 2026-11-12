import { expect, type Page } from "@playwright/test";

// The route's pixels in the strip of map between its top and the sheet's top, from a screenshot (the map's WebGL canvas cannot be read back):
// the line is drawn in the dark style's route colour, #5EE9B5.
export async function routeInStrip(page: Page) {
  const strip = await page.evaluate(() => {
    const map = document.querySelector("[data-trip-map] .maplibregl-map")!.getBoundingClientRect();
    const sheet = document.querySelector("[data-plan-sheet]")!.getBoundingClientRect();
    return { x: Math.max(0, map.left), y: Math.max(0, map.top), w: Math.min(innerWidth, map.right) - Math.max(0, map.left), h: Math.min(map.bottom, sheet.top) - Math.max(0, map.top) };
  });
  expect(strip.h, "the strip of map above the sheet").toBeGreaterThan(20);
  const png = (await page.screenshot({ clip: { x: strip.x, y: strip.y, width: strip.w, height: strip.h }, scale: "css" })).toString("base64");
  const box = await page.evaluate(async (src) => {
    const img = new Image();
    img.src = `data:image/png;base64,${src}`;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width;
    c.height = img.height;
    const g = c.getContext("2d")!;
    g.drawImage(img, 0, 0);
    const d = g.getImageData(0, 0, c.width, c.height).data;
    let n = 0, x0 = c.width, y0 = c.height, x1 = -1, y1 = -1;
    for (let y = 0; y < c.height; y++)
      for (let x = 0; x < c.width; x++) {
        const i = (y * c.width + x) * 4;
        if (Math.abs(d[i] - 0x5e) + Math.abs(d[i + 1] - 0xe9) + Math.abs(d[i + 2] - 0xb5) > 60) continue;
        n++;
        x0 = Math.min(x0, x); y0 = Math.min(y0, y); x1 = Math.max(x1, x); y1 = Math.max(y1, y);
      }
    return { n, w: c.width, h: c.height, span: Math.max(x1 - x0, y1 - y0) };
  }, png);
  return { ...box, strip };
}
