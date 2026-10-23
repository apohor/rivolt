import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { DEMO_PLAN, stubApi } from "./stub";

// Planner: a compact route chooser instead of stacked alternative cards.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const alt = {
  ...DEMO_PLAN.Routes[0],
  TotalTripDurationSec: 16_800,
  TotalChargingDurationSec: 3000,
  ArrivalSoC: 31,
  Waypoints: [
    DEMO_PLAN.Routes[0].Waypoints[0],
    { ...DEMO_PLAN.Routes[0].Waypoints[1], Name: "Sample Fast Charge, Baker", ArrivalSoC: 45, DepartureSoC: 70 },
    { ...DEMO_PLAN.Routes[0].Waypoints[1], EntityID: "demo-2", Name: "Sample Fast Charge, Primm", ArrivalSoC: 30, DepartureSoC: 45, DistanceFromOriginMeters: 380000 },
    { ...DEMO_PLAN.Routes[0].Waypoints[2], ArrivalSoC: 31, DepartureSoC: 31 },
  ],
};
const plan = { ...DEMO_PLAN, Routes: [DEMO_PLAN.Routes[0], alt], costs: [DEMO_PLAN.costs[0], { ...DEMO_PLAN.costs[0], dcfc_spend_low: 17.9, dcfc_spend_high: 17.9 }] };

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    test(`route chooser · ${v.name} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { demo: true, signedIn: false });
      await page.route(/\/api\/demo\/trips\/la-vegas\/plan$/, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(plan) }));
      await page.goto("/demo/trips/plan");
      await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      const group = page.getByRole("radiogroup", { name: "Route options" });
      await expect(group).toBeVisible();
      await expect(group.getByRole("radio", { name: /Recommended/ })).toHaveAttribute("aria-checked", "true");
      await expect(page.getByText(/Barstow/).first()).toBeVisible();
      await expect(page.getByText(/Primm/)).toHaveCount(0);
      await page.screenshot({ path: path.join(SHOTS, `plan-routes-${v.name}-${theme}-a.png`) });
      await group.getByRole("radio", { name: /Alternative 1/ }).click();
      await expect(page.getByText(/Primm/).first()).toBeVisible();
      await expect(page.getByText(/Barstow/)).toHaveCount(0);
      await page.waitForTimeout(500);
      await page.screenshot({ path: path.join(SHOTS, `plan-routes-${v.name}-${theme}-b.png`) });
      const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
      const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}
