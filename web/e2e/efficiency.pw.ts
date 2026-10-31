import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Activity → Efficiency (new look): mi/kWh by temperature, speed and drive mode as three
// distribution charts under the period's average; "Not enough data" when there is nothing to chart.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const b = (key: string, label: string, eff: number | null, drives: number, o: Record<string, unknown> = {}) => ({
  key, label, distance: drives * 18, energy_kwh: eff ? (drives * 18) / eff : 0, efficiency: eff, drives, not_enough_data: drives * 18 < 25, ...o,
});
const FULL = {
  units: "imperial", distance_unit: "mi", efficiency_unit: "mi/kWh", min_bucket_distance: 25,
  total: b("total", "All drives", 2.31, 34),
  by_temperature: [b("lt32", "Below 32 °F", 1.62, 6), b("32_50", "32–50 °F", 1.95, 8), b("50_68", "50–68 °F", 2.28, 9), b("68_86", "68–86 °F", 2.52, 8), b("gt86", "Above 86 °F", 2.4, 1)],
  by_speed: [b("city", "City (under 35 mph)", 2.6, 7), b("suburb", "Suburban (35–55 mph)", 2.45, 10), b("highway", "Highway (55–70 mph)", 2.15, 9), b("fast", "Fast (70+ mph)", null, 0)],
  by_drive_mode: [b("everyday", "All-Purpose", 2.38, 20), b("conserve", "Conserve", 2.62, 9), b("sport", "Sport", 2.1, 1)],
  excluded: { no_energy: 2, duplicates: 0, no_temperature: 1, no_drive_mode: 0 },
};

async function open(page: Page, body: unknown | null, status = 200) {
  await stubApi(page, { scenario: "parked" });
  await page.route("**/api/insights/efficiency*", (r) =>
    status === 404 ? r.fulfill({ status: 404, json: { error: "not found" } }) : r.fulfill({ json: body }),
  );
  await page.goto("/activity?view=efficiency");
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`efficiency distribution charts · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
        localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
      }, [theme]);
      await open(page, FULL);
      await expect(page.locator("[data-eff-state='ready']")).toBeVisible();
      await expect(page.locator("[data-eff-overall]")).toContainText("2.31");
      await expect(page.locator("[data-eff-chart]")).toHaveCount(3);
      // Bars for buckets with enough distance, hatched bars for thin ones, an empty slot for none.
      expect(await page.locator("[data-eff-chart]").nth(0).locator("[data-eff-bar]:not([data-thin])").count()).toBe(4);
      expect(await page.locator("[data-eff-chart]").nth(0).locator("[data-eff-bar][data-thin]").count()).toBe(1);
      expect(await page.locator("[data-eff-chart]").nth(1).locator("[data-eff-empty]").count()).toBe(1);
      await expect(page.locator("[data-eff-chart]").nth(1).locator("[data-eff-avg]")).toBeVisible();
      await expect(page.locator("[data-eff-chart]").nth(0).locator("li").first()).toHaveAttribute("aria-label", /Below 32 °F: 1\.62 mi\/kWh, 6 drives/);
      // Labels come from the server, split into a name and a range for the speed bands.
      await expect(page.locator("[data-eff-chart]").nth(1)).toContainText("Highway");
      await expect(page.locator("[data-eff-chart]").nth(1)).toContainText("55–70 mph");
      // The bars order the way the numbers do (taller is more efficient).
      const hs = await page.locator("[data-eff-chart]").nth(0).locator("[data-eff-bar]:not([data-thin])").evaluateAll((els) => els.map((e) => e.getBoundingClientRect().height));
      expect(hs[0]).toBeLessThan(hs[3]);
      // No two text labels in a chart overlap (value labels, the average label, axis labels).
      const overlaps = await page.evaluate(() => {
        const out: string[] = [];
        document.querySelectorAll("[data-eff-chart]").forEach((chart, ci) => {
          const items: { t: string; r: DOMRect }[] = [];
          chart.querySelectorAll("span, p, div").forEach((el) => {
            if (el.children.length > 0 && !(el as HTMLElement).hasAttribute("data-eff-avg-label")) return;
            const t = (el.textContent || "").trim();
            if (!t || t === "—") return;
            const r = el.getBoundingClientRect();
            if (r.width > 0 && r.height > 0) items.push({ t, r });
          });
          for (let i = 0; i < items.length; i++)
            for (let j = i + 1; j < items.length; j++) {
              const a = items[i].r, b = items[j].r;
              if (a.left < b.right - 0.5 && b.left < a.right - 0.5 && a.top < b.bottom - 0.5 && b.top < a.bottom - 0.5) out.push(`chart ${ci}: "${items[i].t}" x "${items[j].t}"`);
            }
        });
        return out;
      });
      expect(overlaps).toEqual([]);
      await page.waitForTimeout(400);
      await page.screenshot({ path: path.join(SHOTS, `efficiency-${w}-${theme}.png`), fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container, .maplibregl-map").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("not enough data says so, and an older server says it isn't available", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  let page = await ctx.newPage();
  await open(page, { ...FULL, total: { ...FULL.total, efficiency: 2.2, not_enough_data: true, drives: 2 } });
  await expect(page.locator("[data-eff-state='not-enough']")).toContainText("Not enough data yet");
  await expect(page.locator("[data-eff-chart]")).toHaveCount(0);
  await page.close();
  page = await ctx.newPage();
  await open(page, null, 404);
  await expect(page.locator("[data-eff-state='unavailable']")).toBeVisible();
  await ctx.close();
});

test("the view switcher shows Efficiency", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  await stubApi(page, { scenario: "parked" });
  await page.goto("/activity");
  await expect(page.getByRole("button", { name: "Efficiency", exact: true })).toHaveCount(1);
  await ctx.close();
});
