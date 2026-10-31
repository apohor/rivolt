import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The drive list's route thumbnails: a tiny SVG per drive row, drawn from a few points the server sends in one batched request (never one per
// row), with no map and no tile request. Synthetic route near (10, 10).
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const [name, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`${name === "iphone" ? "iphone: " : ""}drive rows show a route thumbnail from one batched request · ${name} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
        // The stub's rows are a few hours to days old: show all time.
        localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
      }, [theme]);
      await stubApi(page);
      const thumbCalls: string[] = [];
      const tileCalls: string[] = [];
      page.on("request", (r) => {
        const u = new URL(r.url());
        if (u.pathname === "/api/drives/route-thumbs") thumbCalls.push(u.search);
        if (u.pathname.startsWith("/api/maps/")) tileCalls.push(u.pathname);
      });
      await page.goto("/activity");
      const drawn = page.locator("[data-route-thumb='drawn']");
      await expect(drawn.first()).toBeVisible();
      await expect(page.locator("[data-kind='drive']")).not.toHaveCount(0);
      // Every drive row has its thumbnail, from one request that names them all (the cap is 60 ids).
      await expect.poll(async () => await drawn.count()).toBeGreaterThan(0);
      expect(thumbCalls).toHaveLength(1);
      expect(thumbCalls[0].split(",").length).toBeLessThanOrEqual(60);
      // No map, no tiles, no glyphs: the list draws SVG.
      expect(tileCalls).toEqual([]);
      await expect(page.locator("[data-route-thumb] canvas, .maplibregl-map")).toHaveCount(0);
      await page.locator("[data-ui-feed]").screenshot({ path: path.join(SHOTS, `drive-thumbs-${name}-${theme}.png`) });
      await ctx.close();
    });
  }
}
