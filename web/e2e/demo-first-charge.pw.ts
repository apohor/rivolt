import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The public demo shows the sample truck's first charge (state A) and the Three things card as worked examples,
// without any account call. Hide lasts for the visit only.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`demo Now shows the first-charge card and Three things · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page, { demo: true, signedIn: false });
    const calls: string[] = [];
    page.on("request", (r) => {
      const p = new URL(r.url()).pathname;
      if (/\/api\/(push|settings\/(engagement|statement)|lease|milestones)/.test(p)) calls.push(p);
    });
    await page.goto("/demo");
    const card = page.locator("[data-first-charge]");
    await expect(card).toHaveAttribute("data-first-charge", "A");
    await expect(card).toContainText("Your first charge");
    await expect(card.getByRole("link", { name: "See the receipt" })).toHaveAttribute("href", /^\/demo\/charges\//);
    await expect(card.locator("[data-first-charge-row]")).toHaveCount(0);
    const three = page.locator("[data-three-things]");
    await expect(three.locator("[data-three-things-row]")).toHaveCount(3);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `demo-first-charge-${v.n}-dark.png`), fullPage: true });
    await card.getByRole("button", { name: "Hide this" }).click();
    await expect(card).toHaveCount(0);
    expect(calls).toEqual([]);
    await ctx.close();
  });
}
