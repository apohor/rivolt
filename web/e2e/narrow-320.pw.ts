import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// A 320 px phone with the text at 200%: nothing on Now runs past the screen or into its neighbour.
for (const [name, opts] of [
  ["parked", { scenario: "parked" }],
  ["driving", { scenario: "driving" }],
  ["charging", { scenario: "charging" }],
] as const) {
 for (const wide of [false, true]) {
  // wide: a wider face than the one the shots use (CI's fonts differ from a laptop's).
  test(`Now at 320 px and 200% text has no overflow (${name}${wide ? ", wide font" : ""})`, async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 700 });
    await stubApi(page, { nowEndpoint: true, ...opts, designPreview: ["now_v2", "now_tidy", "now_slot", "now_drive_graph"], liveDestination: "trip", driveCase: { elapsedMin: 22, mph: (m: number) => 50 + 5 * Math.sin(m), soc: (m: number) => 71 - m * 0.18 } });
    await page.goto("/");
    await page.addStyleTag({ content: "html { font-size: 200% !important; }" + (wide ? " body, body * { font-family: Georgia, serif !important; letter-spacing: 0.04em; }" : "") });
    const month = page.getByTestId("now-month");
    await expect(month).toBeVisible();
    await page.waitForTimeout(500);
    // Fixed bars would sit over the shots.
    await page.addStyleTag({ content: ".fixed, .sticky, [class*='fixed'], [class*='sticky'] { visibility: hidden !important; }" });
    if (!wide) await page.screenshot({ path: `test-results/narrow-320-${name}.png`, fullPage: true, clip: { x: 0, y: 0, width: 320, height: 1800 } });
    const bad = await page.evaluate(() => {
      const w = document.documentElement.clientWidth;
      const out: string[] = [];
      if (document.documentElement.scrollWidth > w) out.push(`page scrolls sideways: ${document.documentElement.scrollWidth} > ${w}`);
      for (const el of document.querySelectorAll<HTMLElement>("main *")) {
        const r = el.getBoundingClientRect();
        if (r.width && r.right > w + 1 && !el.closest("[data-keep-row]") ) out.push(`${el.tagName} ${(el.className + "").slice(0, 60)} left=${Math.round(r.left)} right=${Math.round(r.right)} parent=${Math.round(el.parentElement!.getBoundingClientRect().right)} ${el.parentElement!.className.slice(0,80)}`);
        // Text cut off by its own box (a clipped label), unless it is meant to ellipsize one-liners we allow to wrap.
        if (el.children.length === 0 && el.clientWidth > 0 && el.textContent?.trim() && el.scrollWidth > el.clientWidth + 1 && getComputedStyle(el).overflow !== "visible")
          out.push(`clipped "${el.textContent.trim().slice(0, 30)}" ${el.scrollWidth} > ${el.clientWidth}`);
      }
      return out;
    });
    expect(bad.slice(0, 12)).toEqual([]);
    // The destination line says all of it, wrapped.
    if (name === "driving") {
      const dest = page.getByTestId("now-drive-dest");
      await expect(dest).toBeVisible();
      expect(await dest.evaluate((e) => e.scrollWidth <= e.clientWidth + 1 && getComputedStyle(e).textOverflow !== "ellipsis")).toBe(true);
    }
    // No unit is split: a stat's last word is on one line (one client rect).
    const split = await page.getByTestId("now-slot").locator("p span.inline-block").evaluateAll((els) => els.filter((e) => e.getClientRects().length > 1).map((e) => e.textContent));
    expect(split).toEqual([]);
    // Each stat in the slot stays inside its own cell, too: the unit does not run into the next stat.
    const over = await page.getByTestId("now-slot").locator("[data-testid^='now-slot-']").evaluateAll((cells) =>
      cells.flatMap((c) => {
        const cr = c.getBoundingClientRect();
        return [...c.querySelectorAll("p, span")].filter((e) => e.getBoundingClientRect().right > cr.right + 0.5).map((e) => e.textContent);
      }),
    );
    expect(over).toEqual([]);
    if (!wide) await month.screenshot({ path: `test-results/narrow-320-${name}-month.png` });
    // The month's four figures: each label stays inside its own cell.
    const cells = await month.locator("div.flex-col").evaluateAll((els) =>
      els.map((c) => {
        const cr = c.getBoundingClientRect();
        return [...c.children].every((s) => {
          const r = s.getBoundingClientRect();
          return r.left >= cr.left - 1 && r.right <= cr.right + 1;
        });
      }),
    );
    expect(cells.every(Boolean), `month cells ${cells}`).toBe(true);
    // The charts' day labels (the bottom row of each chart) do not run into each other.
    const touching = await month.locator("svg").evaluateAll((svgs) =>
      svgs.flatMap((svg) => {
        const sb = svg.getBoundingClientRect();
        const row = [...svg.querySelectorAll("text")].map((t) => ({ t: t.textContent, r: t.getBoundingClientRect() })).filter((x) => x.r.width > 0 && sb.bottom - x.r.bottom < 12);
        row.sort((a, b) => a.r.left - b.r.left);
        return row.flatMap((x, i) => (i > 0 && x.r.left < row[i - 1].r.right + 2 ? [`${row[i - 1].t} / ${x.t}`] : []));
      }),
    );
    expect(touching, "axis labels touch").toEqual([]);
  });
 }
}
