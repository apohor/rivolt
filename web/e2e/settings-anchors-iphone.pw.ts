import { expect, test, type Locator, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Deep links into Settings on an iPhone (new look, tab-bar shell): the card's heading lands below the sticky top
// bars (the nav and the page's title row), not under them, and its control sits above the bottom tab bar.
// qa on preview at 390 px WebKit: #lease hid the "Lease" heading under the sticky header and "Add lease" under
// the tab bar; #statement-email opened Account with no card.

type Case = { path: string; card: string; control: (p: Page) => Locator };

const CASES: Case[] = [
  { path: "/settings?tab=vehicle#lease", card: "lease", control: (p) => p.getByRole("button", { name: "Add lease" }) },
  { path: "/settings#statement-email", card: "statement-email", control: (p) => p.locator("#statement-email-toggle") },
];

// Where the sticky bars end and the bottom tab bar starts, in viewport pixels.
async function layout(page: Page, card: string, control: Locator) {
  const bars = await page.evaluate(() => {
    let stuck = 0;
    document.querySelectorAll<HTMLElement>("header, [data-ui-header]").forEach((el) => {
      const pos = getComputedStyle(el).position;
      if (pos === "sticky" || pos === "fixed") stuck = Math.max(stuck, el.getBoundingClientRect().bottom);
    });
    const tabBar = document.querySelector<HTMLElement>('nav[aria-label="Primary"].fixed');
    return { stuck, tabBarTop: tabBar ? tabBar.getBoundingClientRect().top : window.innerHeight };
  });
  const heading = await page.locator(`#${card} > header`).boundingBox();
  const ctl = await control.boundingBox();
  return { ...bars, headingTop: heading?.y ?? -1, controlTop: ctl?.y ?? -1, controlBottom: ctl ? ctl.y + ctl.height : 1e9 };
}

for (const c of CASES) {
  test(`iphone: ${c.path} lands on the card with its control in view`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
    await stubApi(page, { uiNext: true });
    await page.goto(c.path);

    // The anchor opens the tab that owns the card (only that tab's cards are rendered).
    await expect(page.locator(`#${c.card}`)).toBeVisible();
    const control = c.control(page);
    await expect(control).toBeVisible();

    // The alignment keeps re-pinning while cards above fill in; wait for it to settle.
    await expect
      .poll(async () => {
        const g = await layout(page, c.card, control);
        return g.headingTop >= g.stuck - 1 && g.controlTop >= g.stuck - 1 && g.controlBottom <= g.tabBarTop + 1;
      }, { timeout: 8000 })
      .toBe(true);
    const g = await layout(page, c.card, control);
    // Landed just below the bars, not left wherever the page happened to be.
    expect(g.headingTop - g.stuck, JSON.stringify(g)).toBeLessThan(40);
    await ctx.close();
  });
}
