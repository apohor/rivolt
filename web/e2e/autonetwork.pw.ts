import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { currentMonth, ledgerMonthFixture, ledgerSummaryFixture } from "../src/lib/ledger";

// DCFC auto-network pricing: Pricing card (estimate, Confirm / Change,
// why), Money badge + "Estimated, confirm" rows.

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const base = (n: number, o: Record<string, unknown>) => ({
  ID: `charge-${n}`, VehicleID: VEHICLE_ID,
  StartedAt: new Date(Date.now() - 2 * 86_400_000).toISOString(),
  EndedAt: new Date(Date.now() - 2 * 86_400_000 + 1_800_000).toISOString(),
  StartSoCPct: 20, EndSoCPct: 80, EnergyAddedKWh: 52.4, MilesAdded: 150, MaxPowerKW: 180, AvgPowerKW: 110,
  FinalState: "charging_complete", Lat: 40.9, Lon: -100.9, Source: "live", Currency: "USD", ...o,
});

async function setup(page: Page, state: string | null, extra: Record<string, unknown> = {}) {
  await stubApi(page);
  const c2 = base(2, {
    Cost: 20.96, PricePerKWh: 0.4, CostSource: "network_estimate",
    network_state: state, network_confidence: 0.6,
    network_evidence: {
      v: 1, source: "archive",
      candidates: [
        { network: "Tesla Supercharger", slug: "tesla_sc", dist_m: 38 },
        { network: "Rivian Adventure Network", slug: "ran", dist_m: 112 },
      ],
      osm: { network: "tesla", agree: true }, pos: "open", rate_basis: "member",
    },
    ...extra,
  });
  const c3 = base(3, { Cost: 0, PricePerKWh: 0, CostSource: "", network_state: "needs_confirmation", network_confidence: 0 });
  await page.route("**/api/charges?**", (r) => r.fulfill({ json: [base(1, { Cost: 4.4, PricePerKWh: 0.11, CostSource: "home_rate" }), c2, c3] }));
  await page.route("**/api/settings/charging/networks", (r) =>
    r.fulfill({
      json: [
        { name: "Tesla Supercharger", price_per_kwh: 0.5, member_price_per_kwh: 0.4, member_active: true, currency: "USD", slug: "tesla_sc" },
        { name: "Electrify America", price_per_kwh: 0.48, currency: "USD", slug: "ea" },
      ],
    }),
  );
}

for (const [vpName, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`pricing card · ${vpName} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await setup(page, "needs_confirmation");
      let confirmed = 0;
      await page.route("**/api/charges/charge-2/pricing/confirm", (r) => {
        confirmed += 1;
        return r.fulfill({ json: {} });
      });
      await page.goto("/charges/charge-2");
      const panel = page.getByTestId("network-estimate");
      await expect(panel).toBeVisible();
      await expect(panel).toContainText("Please confirm");
      await expect(panel).toContainText("Priced as Tesla Supercharger");
      await expect(panel).toContainText("Nearest: Tesla Supercharger, 38 m; also Rivian Adventure Network, 112 m");
      await panel.getByRole("button", { name: "Change" }).click();
      await expect(panel).toContainText("Rivolt will remember this for this location");
      await panel.scrollIntoViewIfNeeded();
      await panel.screenshot({ path: path.join(SHOTS, `autonet-card-${vpName}-${theme}.png`) });
      await panel.getByRole("button", { name: "Cancel" }).click();
      await panel.getByRole("button", { name: "Confirm" }).click();
      await expect.poll(() => confirmed).toBe(1);
      await ctx.close();
    });
  }
}

test("unmatched charge asks for a network; confirmed shows a quiet tick; flag off shows nothing new", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  await setup(page, "confirmed");
  await page.goto("/charges/charge-3");
  await expect(page.getByTestId("network-estimate")).toContainText("couldn't match a network");
  await expect(page.getByLabel("Prefill from network")).toBeVisible();
  await page.goto("/charges/charge-2");
  await expect(page.getByTestId("network-confirmed")).toContainText("Confirmed");
  await expect(page.getByTestId("network-estimate")).toHaveCount(0);
  await ctx.close();

  const ctx2 = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const p2 = await ctx2.newPage();
  await setup(p2, null);
  await p2.route("**/api/charges?**", (r) => r.fulfill({ json: [base(2, { Cost: 18.2, PricePerKWh: 0.4, CostSource: "network_estimate" })] }));
  await p2.goto("/charges/charge-2");
  await expect(p2.getByRole("heading", { name: "Pricing" })).toBeVisible();
  await expect(p2.getByTestId("network-estimate")).toHaveCount(0);
  await ctx2.close();
});

for (const [vpName, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`money badge + rows · ${vpName} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await setup(page, "estimated");
      await page.route("**/api/ledger/summary**", (r) =>
        r.fulfill({ json: { ...ledgerSummaryFixture(12), to_confirm_count: 3, needs_attention_count: 1 } }),
      );
      await page.route("**/api/ledger/monthly**", (r) => {
        const m = ledgerMonthFixture(currentMonth());
        for (const rc of m.receipts) {
          if (rc.cost_source === "network_estimate") {
            rc.network_state = "needs_confirmation";
            rc.sessions.forEach((s) => (s.network_state = "needs_confirmation"));
          }
        }
        return r.fulfill({ json: m });
      });
      await page.goto("/money");
      const badge = page.getByRole("button", { name: /3 charges to confirm/ });
      await expect(badge).toBeVisible();
      await badge.click();
      await expect(page.getByText(/Estimated, confirm/).first()).toBeVisible();
      await expect(page.getByRole("link", { name: "Confirm →" }).first()).toBeVisible();
      await page.screenshot({ path: path.join(SHOTS, `autonet-money-${vpName}-${theme}.png`), fullPage: false });
      await ctx.close();
    });
  }
}
