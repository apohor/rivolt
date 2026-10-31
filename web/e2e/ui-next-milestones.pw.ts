import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// A milestone on Now: the server's words, newest first, one at a time, dismissible.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const M = [
  { vehicle_id: VEHICLE_ID, key: "miles_1000", kind: "miles_1000", achieved_at: "2026-10-17T12:00:00Z", title: "1,000 miles", body: "1,000 miles recorded in Rivolt: 2.50 mi/kWh, $0.04/mi" },
  { vehicle_id: VEHICLE_ID, key: "charges_100", kind: "charges_100", achieved_at: "2026-10-10T12:00:00Z", title: "100 charges", body: "100 charges recorded in Rivolt" },
];

async function open(page: Page, opts: { status?: number; dismissStatus?: number } = {}) {
  await stubApi(page);
  const dismissed: Record<string, unknown>[] = [];
  await page.route("**/api/milestones", (r) =>
    r.fulfill({ status: opts.status ?? 200, contentType: "application/json", body: JSON.stringify(opts.status === 404 ? { error: "not found" } : { milestones: M }) }),
  );
  await page.route("**/api/milestones/dismiss", (r) => {
    dismissed.push(r.request().postDataJSON() as Record<string, unknown>);
    return r.fulfill({ status: opts.dismissStatus ?? 200, contentType: "application/json", body: JSON.stringify(opts.dismissStatus ? { error: "no" } : { ok: true }) });
  });
  await page.goto("/");
  return dismissed;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`a milestone shows newest first and hides one at a time · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const dismissed = await open(page);
    const card = page.locator("[data-milestone]");
    await expect(card).toContainText("1,000 miles");
    await expect(card).toContainText("1,000 miles recorded in Rivolt: 2.50 mi/kWh, $0.04/mi");
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SHOTS, `milestone-${v.n}-dark.png`) });
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".maplibregl-map").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await card.getByRole("button", { name: "Hide this" }).click();
    await expect.poll(() => dismissed.at(-1)).toEqual({ vehicle_id: VEHICLE_ID, key: "miles_1000" });
    await expect(card).toContainText("100 charges");
    await card.getByRole("button", { name: "Hide this" }).click();
    await expect.poll(() => dismissed.at(-1)).toEqual({ vehicle_id: VEHICLE_ID, key: "charges_100" });
    await expect(page.locator("[data-milestone]")).toHaveCount(0);
    await ctx.close();
  });
}

test("a server without milestones shows nothing", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, { status: 404 });
  await expect(page.getByRole("heading", { name: /Now|Rivolt/ }).first()).toBeVisible().catch(() => undefined);
  await page.waitForTimeout(800);
  await expect(page.locator("[data-milestone]")).toHaveCount(0);
  await ctx.close();
});

test("if hiding fails, the milestone comes back and says so", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, { dismissStatus: 500 });
  const card = page.locator("[data-milestone]");
  await card.getByRole("button", { name: "Hide this" }).click();
  await expect(card).toContainText("1,000 miles");
  await expect(card.getByRole("alert")).toContainText("Couldn't hide that");
  await ctx.close();
});
