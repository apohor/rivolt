import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The Rivian connect step tells you what Rivolt records, above the submit button, and links the
// privacy policy (a new tab, so the typed credentials stay put).
for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`connect step privacy line · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { rivian: "disconnected" });
      await page.goto("/settings?tab=account");
      const line = page.locator("[data-connect-privacy]");
      await expect(line).toBeVisible();
      await expect(line).toContainText("Rivolt records your vehicle's GPS location and other vehicle data to build your drives and charges. See the Privacy policy.");
      const link = line.getByRole("link", { name: "Privacy policy" });
      await expect(link).toHaveAttribute("href", "/privacy");
      await expect(link).toHaveAttribute("target", "_blank");
      await expect(link).toHaveAttribute("rel", "noopener noreferrer");
      // Above the submit button.
      const [lb, sb] = await Promise.all([line.boundingBox(), page.getByRole("button", { name: /^Sign in/ }).first().boundingBox()]);
      expect(lb!.y + lb!.height).toBeLessThanOrEqual(sb!.y);
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}
