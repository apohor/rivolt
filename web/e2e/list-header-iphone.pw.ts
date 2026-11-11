import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The Charges and Drives headers at iPhone width (390 px): the title and the Table/Map + period controls never overlap (#1830).
for (const [name, href] of [
  ["Charges", "/charges"],
  ["Drives", "/drives"],
] as const) {
  test(`${name} header at 390 px: the title and its controls do not overlap @webkit`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.goto(href);
    const title = page.getByRole("heading", { level: 1, name });
    await expect(title).toBeVisible();
    const header = page.locator("[data-ui-header]").filter({ has: title });
    // The Table/Map toggle and the period picker, by their outer boxes (the picker's border box is what covered the title's last letter).
    const controls = [
      header.getByRole("button", { name: "Map", exact: true }).locator(".."),
      header.getByRole("button", { name: "All", exact: true }).locator(".."),
    ];
    // The heading's text box, not its block: an h1 shrunk under the controls keeps a narrow block while its text runs on under them.
    const text = await title.evaluate((h) => {
      const r = document.createRange();
      r.selectNodeContents(h);
      const b = r.getBoundingClientRect();
      return { x: b.x, y: b.y, width: b.width, height: b.height };
    });
    for (const c of controls) {
      const b = await c.boundingBox();
      expect(b, "control is laid out").not.toBeNull();
      const overlaps = text.x < b!.x + b!.width && b!.x < text.x + text.width && text.y < b!.y + b!.height && b!.y < text.y + text.height;
      expect(overlaps, `${name} title text ${JSON.stringify(text)} vs control ${JSON.stringify(b)}`).toBe(false);
    }
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(1);
    await ctx.close();
  });
}

// The Charges and Drives tables at 390 px drop to the phone columns, as on Activity, so SoC is on screen without a sideways scroll (#1848).
for (const [name, href] of [
  ["Charges", "/charges"],
  ["Drives", "/drives"],
] as const) {
  test(`${name} table at 390 px: SoC is on screen and the table does not scroll sideways @webkit`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 });
    const page = await ctx.newPage();
    await page.addInitScript(() => localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 })));
    await stubApi(page);
    await page.goto(href);
    const soc = page.locator("main table th[data-col='soc']");
    await expect(soc).toBeVisible();
    const b = await soc.boundingBox();
    expect(b!.x + b!.width, "SoC header ends inside the viewport").toBeLessThanOrEqual(390);
    const scroll = await page.locator("main table").first().evaluate((t) => {
      const w = t.closest(".overflow-x-auto") as HTMLElement;
      return w.scrollWidth - w.clientWidth;
    });
    expect(scroll, "table scrolls sideways").toBeLessThanOrEqual(1);
    await ctx.close();
  });
}
