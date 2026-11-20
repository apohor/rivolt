import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The share card's optional map (design_preview "share_map"): off every time the sheet opens, and when the tile archive cannot be read the card
// still saves with the plain line; whatever the map asks for stays on the page's own origin. (The real basemap is looked at in the preview
// shots; the style audit and the camera are in src/map/shareBasemap.test.ts.)
const SEGMENTS = ["_p~iF~ps|U_ulLnnqC_mqNvxq`@"];

for (const [name, size] of [["phone", { width: 393, height: 852 }], ["desktop", { width: 1280, height: 900 }]] as const) {
  test(`the map toggle is off on every open, and an unreadable archive leaves the plain line (${name})`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: size, colorScheme: "dark", acceptDownloads: true });
    const page = await ctx.newPage();
    await stubApi(page, { designPreview: ["share_map"] });
    await page.route("**/api/maps/tiles/us.pmtiles", (r) => r.fulfill({ status: 404, body: "" }));
    await page.route("**/api/drives/drive-4/postcard", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ version: 1, distance_mi: 34, route: { segments: SEGMENTS } }) }));
    await page.goto("/drives/drive-4");
    const open = async () => {
      await page.getByRole("button", { name: "Share trip" }).click();
      return page.getByRole("dialog");
    };
    let dlg = await open();
    const box = dlg.getByRole("checkbox", { name: /Include map/ });
    await expect(box).not.toBeChecked();
    await expect(dlg).toContainText("Adds a map of the area you drove through, without place names. The start and end are cut off, and so is the area around your saved home.");
    const requests: string[] = [];
    page.on("request", (r) => requests.push(r.url()));
    await box.check();
    const download = page.waitForEvent("download");
    await dlg.getByRole("button", { name: "Save image" }).click();
    expect((await download).suggestedFilename()).toMatch(/^rivolt-trip-\d{4}-\d{2}\.png$/);
    await expect(dlg.getByRole("status")).toContainText("Saved rivolt-trip-");
    // Every request is on the page's own origin; nothing asks for a font, a sprite, terrain or radar.
    const origin = new URL(page.url()).origin;
    for (const u of requests) {
      expect(u.startsWith(origin + "/"), u).toBe(true);
      expect(u).not.toMatch(/glyphs|sprite|\/dem\/|radar/);
    }
    await dlg.getByRole("button", { name: "Cancel" }).click();
    dlg = await open();
    await expect(dlg.getByRole("checkbox", { name: /Include map/ })).not.toBeChecked();
    await ctx.close();
  });
}
