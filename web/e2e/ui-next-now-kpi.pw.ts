import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// ui_theme_next Now: KPI values stay on one line ("2.30 mi/kWh" wrapped at 1280/1440 when the
// 24px type scale landed). The unit is a 12px suffix. Flag off keeps the old markup.
for (const w of [390, 1280, 1440] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`Now KPI values are one line · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { scenario: "parked", drain: "ok" });
      await page.goto("/");
      await expect(page.getByText("Efficiency", { exact: true })).toBeVisible();
      await page.waitForTimeout(600);
      const res = await page.evaluate(() => {
        const out: { t: string; lines: number }[] = [];
        document.querySelectorAll("[data-ui-area='kpi'] .text-xl").forEach((el) => {
          const cs = getComputedStyle(el);
          const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.3;
          out.push({ t: (el.textContent || "").trim(), lines: Math.round(el.getBoundingClientRect().height / lh) });
        });
        return out;
      });
      expect(res.length).toBeGreaterThan(3);
      for (const r of res) expect(r.lines, r.t).toBe(1);
      expect(res.some((r) => /mi\/kWh/.test(r.t))).toBe(true);
      await ctx.close();
    });
  }
}

test("flag off: the efficiency value is unchanged plain text", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "off"));
  await stubApi(page, { scenario: "parked", drain: "ok" });
  await page.goto("/");
  await expect(page.getByText(/2\.50 mi\/kWh/)).toBeVisible();
  await expect(page.locator("[data-ui-area='kpi'] .whitespace-nowrap")).toHaveCount(0);
});
