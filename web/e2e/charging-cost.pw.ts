import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// /charging-cost: public and signed out. A worked example (every figure an example), a calculator that runs in the page, and the
// fast-charge price only while its month is under a year old.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

// The server puts the tracker's price in the page at serve time; this stands in for it (or leaves it out).
async function open(page: Page, now = "2026-11-01T12:00:00", fast: object | null = { fast_price: 0.48, price_month: "2026-10" }) {
  if (fast) {
    await page.addInitScript((data) => {
      const obs = new MutationObserver(() => {
        if (!document.body) return;
        const el = document.createElement("script");
        el.type = "application/json";
        el.id = "charging-cost";
        el.textContent = JSON.stringify(data);
        document.body.appendChild(el);
        obs.disconnect();
      });
      obs.observe(document, { childList: true, subtree: true });
    }, fast);
  }
  await stubApi(page, { signedIn: false });
  await page.clock.setFixedTime(new Date(now));
  await page.goto("/charging-cost");
}

for (const v of [
  { w: 390, scheme: "dark" as const },
  { w: 1280, scheme: "light" as const },
]) {
  test(`charging cost: example, calculator, no requests · ${v.w}px ${v.scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: v.w, height: 900 }, colorScheme: v.scheme });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [v.scheme]);
    await open(page);
    await expect(page.getByRole("heading", { level: 1, name: "How much does it cost to charge a Rivian at home?" })).toBeVisible();
    const ex = page.locator("[data-cc-example]");
    await expect(ex).toContainText("1,000 ÷ 2.3 ≈ 435 kWh");
    await expect(ex).toContainText("$65 per month");
    await expect(ex).toContainText("6.5 cents per mile");
    await expect(ex).toContainText("(example)");
    await expect(page.getByText("$44 instead of $65")).toBeVisible();
    const fast = page.locator("[data-cc-fast]");
    await expect(fast).toContainText("typical price seen by DCFC Tracker");
    await expect(fast).toContainText("Oct 2026");
    await expect(fast.getByRole("link", { name: "DCFC Tracker" })).toHaveAttribute("href", "https://dcfctracker.com/");
    await expect(page.locator("main")).not.toContainText(/\bgas\b|most owners|Rivian app/i);

    // The calculator: client-side only; nothing is requested while using it.
    const requests: string[] = [];
    page.on("request", (r) => requests.push(r.url()));
    await page.getByRole("button", { name: "Calculate" }).click();
    await expect(page.getByRole("alert").filter({ hasText: "Enter your rate per kWh." })).toBeVisible();
    await page.getByLabel("Your rate per kWh").fill("0.15");
    await page.getByLabel(/Off-peak rate per kWh/).fill("0.10");
    await page.getByRole("button", { name: "Calculate" }).click();
    await expect(page.getByRole("alert").filter({ hasText: "Enter the share of your charging" })).toBeVisible();
    await page.getByLabel(/Share of your charging in off-peak hours/).fill("100");
    await page.getByRole("button", { name: "Calculate" }).click();
    const res = page.locator("[data-cc-result]");
    await expect(res.locator("[data-cc-total]")).toHaveText("$43.48");
    await expect(res.locator("[data-cc-permile]")).toHaveText("4.3 cents");
    await expect(res.locator("[data-cc-saving]")).toHaveText("about $21.74 a month (estimate)");
    expect(requests).toEqual([]);
    // The demo link carries nothing typed here.
    for (const href of await page.locator("main a[href*='demo']").evaluateAll((as) => as.map((a) => (a as HTMLAnchorElement).getAttribute("href")))) {
      expect(href).not.toMatch(/\?|0\.15|rate/);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `charging-cost-${v.w}-${v.scheme}.png`), fullPage: true });
    await ctx.close();
  });
}

test("charging cost: a year after the tracker's month, or with no price from the server, there is no fast-charge price on the page", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, "2027-11-01T12:00:00");
  await expect(page.locator("[data-cc-example]")).toBeVisible();
  await expect(page.locator("[data-cc-fast]")).toHaveCount(0);
  await expect(page.locator("main")).not.toContainText(/DCFC Tracker|\$0\.48/);
  const page2 = await ctx.newPage();
  await open(page2, "2026-11-01T12:00:00", null);
  await expect(page2.locator("[data-cc-fast]")).toHaveCount(0);
  await expect(page2.locator("main")).not.toContainText(/DCFC Tracker|\$0\.48/);
  await page2.close();
  await ctx.close();
});
