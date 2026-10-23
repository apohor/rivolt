import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// Admin Home (new look): "Is everything OK?", from GET /api/admin/health-summary.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ok = { status: "ok" };
const iso = (msAgo: number) => new Date(e2eNow() - msAgo).toISOString();
const healthy = () => ({
  generated_at: iso(5_000), version: "0.36.20",
  alerts: { ...ok, items: [] },
  kill_switch: { ...ok, paused: false },
  rivian: { ...ok, window: "24h", results: { success: 4120, rate_limited: 12, upstream_error: 8 }, error_rate: 0.005, breaker_state: 0, breaker_trips: {}, rate_limited: { rivian: 12 } },
  vehicles: { ...ok, leased: 7, streaming: 7 },
  disk: { ...ok, used_pct: 41.2 },
  backup: { ...ok, age_seconds: 6 * 3600 },
});

async function open(page: Page, summary: unknown, extra?: (p: Page) => Promise<void>) {
  await stubApi(page);
  await extra?.(page);
  let calls = 0;
  await page.route("**/api/admin/health-summary", (r) => { calls++; return r.fulfill({ json: summary }); });
  await page.goto("/admin");
  await expect(page.locator("[data-admin-home]")).toBeVisible();
  return () => calls;
}

async function clean(page: Page, name: string) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
  const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
  expect(summary, summary.join("\n")).toEqual([]);
  await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: true });
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`home, all well · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page, healthy());
      await expect(page.getByRole("tab", { name: "Home" }).or(page.getByRole("button", { name: "Home" }))).toBeVisible();
      await expect(page.locator("[data-verdict]")).toHaveAttribute("data-verdict", "ok");
      await expect(page.getByRole("heading", { name: "Everything looks OK" })).toBeVisible();
      await expect(page.locator("[data-check]")).toHaveCount(6);
      await expect(page.getByText("No alerts firing.")).toBeVisible();
      await expect(page.getByText("7 streaming of 7 leased").first()).toBeVisible();
      await expect(page.getByText("41% used").first()).toBeVisible();
      await expect(page.getByText("6h ago").first()).toBeVisible();
      await expect(page.getByText(/v0\.36\.20/)).toBeVisible();
      await clean(page, `admin-home-ok-${w}-${theme}`);
      await ctx.close();
    });

    test(`home, trouble · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      const s = healthy();
      s.alerts = { ...ok, items: [{ name: "RivolRivianErrorRateHigh", severity: "critical", since: iso(3 * 3_600_000) }, { name: "RivolBackupLate", severity: "warning", since: iso(40 * 60_000) }] } as never;
      s.rivian = { ...s.rivian, error_rate: 0.34, breaker_state: 2, breaker_trips: { rivian: 3 } } as never;
      s.vehicles = { ...ok, leased: 7, streaming: 5 } as never;
      s.backup = { ...ok, age_seconds: 30 * 3600 } as never;
      s.disk = { status: "unavailable", reason: "prometheus not configured" } as never;
      await open(page, s);
      await expect(page.locator("[data-verdict]")).toHaveAttribute("data-verdict", "bad");
      await expect(page.getByRole("heading", { name: /need attention: Alerts, Rivian calls, Vehicles, Backup/ })).toBeVisible();
      await expect(page.locator("[data-check=alerts]")).toHaveAttribute("data-tone", "bad");
      await expect(page.locator("[data-check=rivian]")).toContainText("breaker open");
      await expect(page.locator("[data-check=vehicles]")).toHaveAttribute("data-tone", "warn");
      await expect(page.locator("[data-check=backup]")).toHaveAttribute("data-tone", "warn");
      await expect(page.locator("[data-check=disk]")).toHaveAttribute("data-tone", "unknown");
      await expect(page.getByText("RivolRivianErrorRateHigh")).toBeVisible();
      await expect(page.getByText("Unavailable: prometheus not configured")).toHaveCount(0); // the disk card names it in its own words
      await expect(page.locator("[data-admin-home]")).toContainText("prometheus not configured");
      await clean(page, `admin-home-bad-${w}-${theme}`);
      await ctx.close();
    });
  }
}

test("an unavailable section is a gap, never green", async ({ page }) => {
  const s = healthy();
  s.alerts = { status: "unavailable", reason: "alertmanager unreachable", items: [] } as never;
  s.kill_switch = { status: "unavailable", reason: "no flags store", paused: false } as never;
  await open(page, s);
  await expect(page.locator("[data-verdict]")).toHaveAttribute("data-verdict", "unknown");
  await expect(page.getByRole("heading", { name: /could not be read/ })).toBeVisible();
  await expect(page.getByText("Unavailable: alertmanager unreachable.")).toBeVisible();
  await expect(page.locator("[data-check=alerts]")).toHaveAttribute("data-tone", "unknown");
  await expect(page.getByText("No alerts firing.")).toHaveCount(0);
});

test("the navigation links, the default tab, and the deep link", async ({ page }) => {
  await open(page, healthy(), async (p) => {
    await p.route("**/api/admin/kill-switch", (r) => r.fulfill({ json: { kill_switch: { paused: false }, trip_planner: { enabled: true } } }));
    await p.route("**/api/admin/audit**", (r) => r.fulfill({ json: { items: [], next_before: null } }));
    await p.route("**/api/admin/users", (r) => r.fulfill({ json: { users: [] } }));
  });
  await expect(page.locator("[data-admin-home]")).toBeVisible(); // Home is the default tab in the new look
  await page.getByRole("button", { name: "Kill switch and flags" }).click();
  await expect(page.locator("[data-admin-flags]")).toBeVisible();
  await page.getByRole("button", { name: "Home" }).click();
  await expect(page.locator("[data-admin-home]")).toBeVisible();
  await page.goto("/admin#ai");
  await expect(page.locator("[data-admin-home]")).toHaveCount(0);
});

test("Refresh asks again, and it also refreshes by itself", async ({ page }) => {
  const calls = await open(page, healthy());
  const before = calls();
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect.poll(() => calls()).toBeGreaterThan(before);
});

test("a failed summary says so", async ({ page }) => {
  await stubApi(page);
  await page.route("**/api/admin/health-summary", (r) => r.fulfill({ status: 500, json: { error: "boom" } }));
  await page.goto("/admin");
  await expect(page.getByText("Couldn't load the health summary")).toBeVisible();
});

test("home: the signup funnel by week is counts only, with old weeks' return as a dash", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const s = healthy() as Record<string, unknown>;
  const monday = (daysAgo: number) => new Date(e2eNow() - daysAgo * 86_400_000).toISOString().slice(0, 10);
  s.engagement = {
    ...ok, active_7d: 5, active_28d: 9, connected_no_data: 1, first_data_7d: 2, first_data_28d: 5, median_seconds_to_first_data: 3600, emails_sent: {},
    funnel_weeks: [
      { week: monday(63), signups: 12, connected: 7, first_data: 5, day2_eligible: 12, returned_day2: 1 },
      { week: monday(14), signups: 10, connected: 6, first_data: 4, day2_eligible: 10, returned_day2: 4 },
      { week: monday(3), signups: 3, connected: 1, first_data: 1, day2_eligible: 0, returned_day2: 0 },
    ],
  };
  await open(page, s);
  const f = page.locator("[data-funnel]");
  await expect(f).toBeVisible();
  const rows = f.locator("tbody tr");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(0).locator("td").nth(3)).toHaveText("—");
  await expect(rows.nth(1).locator("td").nth(3)).toHaveText("4 of 10 · 40%");
  await expect(rows.nth(2).locator("td").nth(3)).toHaveText("—");
  await expect(rows.nth(1).locator("td").nth(1)).toHaveText("6 · 60%");
  await clean(page, "admin-funnel-390");
  await ctx.close();
});
