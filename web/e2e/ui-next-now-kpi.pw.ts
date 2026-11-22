import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Now: KPI values stay on one line ("2.30 mi/kWh" wrapped at 1280/1440 when the
// 24px type scale landed). The unit is a 12px suffix. Checked on the month strip.
for (const w of [390, 1280, 1440] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`Now KPI values are one line · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
        // All time, not "this month": in a month's first minutes the month has no
        // drives yet, so the efficiency tile is honestly empty (nightly at 00:15 UTC).
        localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
      }, [theme]);
      await stubApi(page, { scenario: "parked", drain: "ok" });
      await page.goto("/");
      await expect(page.getByTestId("now-month")).toBeVisible();
      await page.waitForTimeout(600);
      const res = await page.evaluate(() => {
        const out: { t: string; lines: number }[] = [];
        document.querySelectorAll("[data-testid='now-month'] .text-xl").forEach((el) => {
          const cs = getComputedStyle(el);
          const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.3;
          out.push({ t: (el.textContent || "").trim(), lines: Math.round(el.getBoundingClientRect().height / lh) });
        });
        return out;
      });
      expect(res.length).toBe(4);
      for (const r of res) expect(r.lines, r.t).toBe(1);
      await expect(page.getByTestId("now-month")).toContainText("mi/kWh");
      await ctx.close();
    });
  }
}

