import fs from "node:fs";
import AxeBuilder from "@axe-core/playwright";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";
import { currentMonth, ledgerMonthFixture, ledgerMonthFromReceipts, ledgerSummaryFixture, type Receipt } from "../src/lib/ledger";
import { e2eNow } from "./clock";

// Money's edge states for users who open it for the first time: nothing
// this month, only home AC, only Rivian-billed, an R2 owner (91.5 kWh pack,
// fewer signals), no home rate set, and a brand-new user with no history.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const month = currentMonth(new Date(e2eNow()));
const fx = () => ledgerMonthFixture(month, new Date(e2eNow()));

type Scenario = {
  name: string;
  receipts: (all: Receipt[]) => Receipt[];
  opts?: { miles?: number; drives?: number; homeRate?: number };
  vehicles?: unknown[];
  charging?: Record<string, unknown>;
  summaryMonths?: boolean;
  text: (string | RegExp)[];
  absent?: (string | RegExp)[];
};

const R2 = [{ id: "01-000000000", vin: "R2VIN", name: "Sample R2", model: "R2", model_year: 2027, pack_kwh: 91.5 }];

const SCENARIOS: Scenario[] = [
  {
    name: "no-charges-this-month",
    receipts: () => [],
    opts: { miles: 212.4, drives: 9 },
    text: [/No charges recorded yet/, /They appear here as you charge/],
    // No "-100% vs last month" or "$0.000/mi" on an empty month.
    absent: [/NaN|undefined|Infinity|vs September|\$0\.000\/mi/],
  },
  {
    name: "home-ac-only",
    receipts: (all) => all.filter((r) => r.kind === "ac" && r.place === "home"),
    text: [],
    absent: [/Estimate pending|Likely wrong|Couldn't identify/],
  },
  {
    name: "rivian-billed-only",
    receipts: (all) => all.filter((r) => r.cost_source === "rivian"),
    text: [],
    absent: [/Estimate pending|Likely wrong|estimated from charging-network/],
  },
  {
    name: "r2-sparse-signals",
    receipts: (all) =>
      all
        .filter((r) => r.kind === "ac")
        .slice(0, 6)
        .map((r) => ({ ...r, peak_kw: null, soc_start: null, soc_end: null, sessions: r.sessions.map((s) => ({ ...s, peak_kw: null, soc_start: null, soc_end: null })) })),
    vehicles: R2,
    text: [],
    absent: [/NaN|undefined|null|Infinity/],
  },
  {
    name: "no-home-rate",
    // Without a home rate the server can't price home charging: those sessions are unpriced.
    receipts: (all) =>
      all
        .filter((r) => r.place === "home")
        .map((r) => ({
          ...r,
          cost: null,
          cost_source: "unknown" as const,
          rate: null,
          flags: [...r.flags, "unpriced" as const],
          sessions: r.sessions.map((x) => ({ ...x, cost: null, price_per_kwh: null, cost_source: "unknown" as const })),
        })),
    opts: { homeRate: 0 },
    charging: { home_price_per_kwh: 0, home_currency: "USD", gas_price_per_gallon: 0, comparison_mpg: 0 },
    absent: [/NaN|undefined|Infinity|\$0\.00 if charged at home|vs September|\$0\.000\/mi/],
    text: [/set it in Settings|Set your home rate/],
  },
  {
    name: "all-unpriced-zero",
    // Every session unpriced and no home rate: the $0 headline must say why.
    receipts: (all) =>
      all.map((r) => ({
        ...r,
        cost: null,
        cost_source: "unknown" as const,
        rate: null,
        flags: [...r.flags, "unpriced" as const],
        sessions: r.sessions.map((x) => ({ ...x, cost: null, price_per_kwh: null, cost_source: "unknown" as const })),
      })),
    opts: { homeRate: 0 },
    charging: { home_price_per_kwh: 0, home_currency: "USD", gas_price_per_gallon: 0, comparison_mpg: 0 },
    absent: [/NaN|undefined|Infinity|Not included: 23 unpriced/],
    text: [/\$0 doesn't mean charging was free/, /Set your home rate/],
  },
  {
    name: "brand-new-user",
    receipts: () => [],
    opts: { miles: 0, drives: 0 },
    charging: { home_price_per_kwh: 0, home_currency: "USD", gas_price_per_gallon: 0, comparison_mpg: 0 },
    vehicles: [],
    summaryMonths: false,
    text: [/Nothing priced yet/, /Charges show up here with their cost/, /Set home rate/],
    absent: [/NaN|undefined|Infinity/],
  },
];

async function setup(page: Page, sc: Scenario) {
  await stubApi(page);
  const m = ledgerMonthFromReceipts(month, sc.receipts(fx().receipts), { ...sc.opts, now: new Date(e2eNow()) });
  await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ json: m }));
  await page.route("**/api/ledger/summary**", (r) =>
    r.fulfill({ json: sc.summaryMonths === false ? { version: 1, tz: m.tz, currency: "USD", months: [] } : ledgerSummaryFixture(12, new Date(e2eNow())) }),
  );
  if (sc.vehicles) await page.route("**/api/vehicles", (r) => r.fulfill({ json: sc.vehicles }));
  if (sc.charging) await page.route("**/api/settings/charging/", (r) => r.fulfill({ json: sc.charging }));
}

for (const sc of SCENARIOS) {
  for (const scheme of ["dark", "light"] as const) {
    test(`money edge: ${sc.name} · iphone · ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: scheme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), scheme);
      const errors: string[] = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await setup(page, sc);
      await page.goto("/money");
      await expect(page.getByRole("heading", { name: "Money" })).toBeVisible();
      if (sc.name.startsWith("no-charges") || sc.name === "brand-new-user") await expect(page.getByText(/No charges recorded yet|Nothing priced yet/).first()).toBeVisible();
      else await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
      for (const t of sc.text) await expect(page.getByText(t).first()).toBeVisible();
      await page.waitForTimeout(500);
      const body = await page.locator("main").innerText();
      for (const a of sc.absent ?? []) expect(body, String(a)).not.toMatch(a);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      expect(overflow).toBeLessThanOrEqual(1);
      await page.screenshot({ path: path.join(SHOTS, `money-edge-${sc.name}-${scheme}.png`), fullPage: true });
      expect(errors).toEqual([]);
      await ctx.close();
    });
  }
}

// The $0 explainer on desktop: readable, no overflow, no axe violations.
for (const scheme of ["dark", "light"] as const) {
  test(`money edge: all-unpriced-zero · desktop · ${scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: scheme });
    const page = await ctx.newPage();
    await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), scheme);
    await setup(page, SCENARIOS.find((s) => s.name === "all-unpriced-zero")!);
    await page.goto("/money");
    const note = page.locator("[data-money-zero]");
    await expect(note).toBeVisible();
    await expect(note.getByRole("link", { name: "Set your home rate" })).toHaveAttribute("href", "/settings?tab=charging#cost");
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
    const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `money-zero-desktop-${scheme}.png`) });
    await ctx.close();
  });
}
