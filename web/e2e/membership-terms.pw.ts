import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Settings > Charging networks: each network's membership plans as the network lists them, with the server's "as listed ... last checked"
// line under every row. Text only, links only to https pages, nothing when there are no rows.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ROWS = [
  { network_slug: "evgo", network: "EVgo", plan: "EVgo Plus", terms: "$6.99 a month; up to 15% off standard rates; free reservations; no session fees.", source_url: "https://www.evgo.com/pricing/", read_via: "page", checked: "2026-10-20", label: "As listed by EVgo; may be out of date; last checked 2026-10-20." },
  { network_slug: "evgo", network: "EVgo", plan: "EVgo PlusMax", terms: "$12.99 a month; up to 30% off standard rates; free reservations; no session fees.", source_url: "https://www.evgo.com/pricing/", read_via: "search snippet", checked: "2026-10-20", label: "As listed by EVgo; may be out of date; last checked 2026-10-20." },
  { network_slug: "ea", network: "Electrify America", plan: "Pass+ <b>x</b>", terms: "Save about 25% on charging compared with guest pricing.", source_url: "https://evil.example/www.electrifyamerica.com/", read_via: "page", checked: "2026-10-20", label: "As listed by Electrify America; may be out of date; last checked 2026-10-20." },
];

async function open(page: Page, respond: (r: import("@playwright/test").Route) => Promise<void>) {
  await stubApi(page);
  await page.route("**/api/settings/charging/membership-terms", respond);
  await page.goto("/settings?tab=charging");
  await expect(page.getByRole("heading", { name: "Charging networks" })).toBeVisible();
}

for (const v of [
  { w: 390, scheme: "dark" as const },
  { w: 1280, scheme: "light" as const },
]) {
  test(`membership terms: rows, the as-listed line, safe links · ${v.w}px ${v.scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: v.w, height: 900 }, colorScheme: v.scheme });
    const page = await ctx.newPage();
    await open(page, (r) => r.fulfill({ json: { terms: ROWS } }));
    const box = page.locator("[data-membership-terms]");
    await expect(box).toBeVisible();
    await expect(box).toContainText("Membership plans, as the networks list them");
    await expect(box.locator("[data-membership-row]")).toHaveCount(3);
    const plus = box.locator("[data-membership-row]").filter({ hasText: "EVgo Plus" }).first();
    await expect(plus).toContainText("As listed by EVgo; may be out of date; last checked 2026-10-20.");
    await expect(plus).toContainText("Read from the network's page.");
    await expect(plus.getByRole("link", { name: "Source" })).toHaveAttribute("href", "https://www.evgo.com/pricing/");
    await expect(box.locator("[data-membership-row]").filter({ hasText: "PlusMax" })).toContainText("Read from a search snippet.");
    // Markup in a plan name is text; an unsafe link is not drawn.
    const ea = box.locator("[data-membership-row]").filter({ hasText: "Electrify America" }).or(box.locator("[data-membership-row]").filter({ hasText: "Pass+" }));
    await expect(ea.first()).toContainText("Pass+ <b>x</b>");
    await expect(ea.first().getByRole("link")).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include("[data-membership-terms]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await box.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `membership-terms-${v.w}-${v.scheme}.png`), fullPage: true });
    await ctx.close();
  });
}

test("membership terms: nothing is drawn when the server has no rows or no endpoint", async ({ browser }) => {
  for (const respond of [(r: import("@playwright/test").Route) => r.fulfill({ json: { terms: [] } }), (r: import("@playwright/test").Route) => r.fulfill({ status: 404, json: { error: "not found" } })]) {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, respond);
    await expect(page.locator("[data-membership-terms]")).toHaveCount(0);
    await ctx.close();
  }
});
