import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Idle-awake values read as minutes under an hour and hours + minutes above,
// on the Truck tab and on Now; nothing is capped.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const scheme of ["dark", "light"] as const) {
  test(`idle-awake chart reads in minutes/hours · iphone · ${scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: scheme });
    const page = await ctx.newPage();
    await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), scheme);
    await stubApi(page);
    // 14 days: 0.75 h (45 min) up to 3.5 h (3 h 30 min) and one 22 h day (not capped).
    await page.route("**/api/sleep-activity**", (r) =>
      r.fulfill({
        json: Array.from({ length: 14 }, (_, i) => ({
          day: new Date(Date.now() - (13 - i) * 86_400_000).toISOString().slice(0, 10) + "T00:00:00Z",
          asleep_h: 12, idle_awake_h: i === 6 ? 22.28 : 0.75 + (i % 4) * 0.9, sleeps: 3, failed_sleeps: 0,
        })),
      }),
    );
    await page.goto("/truck");
    const card = page.locator("section,div").filter({ has: page.getByRole("heading", { name: "Sleep and idle-awake" }) }).last();
    await expect(card.getByText(/averaging \d+ h( \d+ min)? a day|averaging \d+ min a day/)).toBeVisible();
    const axis = (await card.locator("svg text").evaluateAll((els) => els.map((e) => e.textContent ?? ""))).join(" ");
    expect(axis).toMatch(/\d+h|\d+m/);
    expect(axis).not.toMatch(/\b\d{3,}m\b/);
    // The 22 h day is in the data: its tooltip is "22 h 17 min", not capped.
    const titles = await card.locator("svg rect title").evaluateAll((els) => els.map((e) => e.textContent ?? ""));
    expect(titles.some((t) => /22 h 17 min/.test(t))).toBe(true);
    expect(titles.some((t) => /45 min/.test(t))).toBe(true);
    // The cards re-render as the page's queries settle, so the located element
    // can be replaced between "visible" and the scroll: retry the pair together.
    await expect(async () => {
      await card.scrollIntoViewIfNeeded();
      await card.screenshot({ path: path.join(SHOTS, `idle-format-truck-${scheme}.png`) });
    }).toPass();

    await page.goto("/");
    const now = page.locator("section,div").filter({ has: page.getByRole("heading", { name: "Idle-awake time per day" }) }).last();
    await expect(now).toBeVisible();
    await expect(now.locator("svg").first()).toBeVisible();
    await expect(async () => {
      await now.scrollIntoViewIfNeeded();
      await now.screenshot({ path: path.join(SHOTS, `idle-format-now-${scheme}.png`) });
    }).toPass();
    await ctx.close();
  });
}
