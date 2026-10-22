import { expect, test, type Locator, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";

// Deep links into Settings on an iPhone (new look, tab-bar shell): the card's heading lands below the sticky top
// bars (the nav and the page's title row), not under them, and its control sits above the bottom tab bar.
// qa on preview at 390 px WebKit: #lease hid the "Lease" heading under the sticky header and "Add lease" under
// the tab bar; #statement-email opened Account with no card.

type Case = { path: string; card: string; control: (p: Page) => Locator };

const CASES: Case[] = [
  { path: "/settings?tab=vehicle#lease", card: "lease", control: (p) => p.getByRole("button", { name: /^(Add|Save) lease$/ }) },
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
    const atBottom = window.scrollY + window.innerHeight >= document.documentElement.scrollHeight - 2;
    return { stuck, atBottom, tabBarTop: tabBar ? tabBar.getBoundingClientRect().top : window.innerHeight };
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
    // The Lease card waits for the owned vehicles and the (absent) lease before it shows its form.
    await page.route("**/api/vehicles/owned", (r) =>
      r.fulfill({ json: { vehicles: [{ id: "o1", rivian_vehicle_id: VEHICLE_ID, display_name: "Sample R1S", model: "R1S" }] } }),
    );
    await page.route(`**/api/vehicles/${VEHICLE_ID}/lease`, (r) => r.fulfill({ status: 404, json: { error: "not found" } }));
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
    // Landed just below the bars, not left wherever the page happened to be. A short tab can't scroll its card all
    // the way up: then the page is at its end and the card is as high as it can get.
    expect(g.atBottom || g.headingTop - g.stuck < 40, JSON.stringify(g)).toBe(true);
    await ctx.close();
  });
}

// A card that loads its own data (here the setup-emails card, hidden until its call answers) is not in the page when
// the anchor first scrolls. The anchor must still end with it in view once it appears, not leave it below the fold.
// Five loads, since qa saw it only intermittently on preview.
test("iphone: a card that appears late is still scrolled to (5 loads)", async ({ browser }) => {
  for (let load = 0; load < 5; load++) {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 664 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
    await stubApi(page, { uiNext: true });
    await page.route("**/api/settings/onboarding-email", async (r) => {
      await new Promise((res) => setTimeout(res, 300 + load * 250));
      return r.fulfill({ json: { enabled: true } });
    });
    await page.goto("/settings#onboarding-email");
    const card = page.locator("#onboarding-email");
    await expect(card).toBeVisible();
    await expect
      .poll(async () => {
        const g = await layout(page, "onboarding-email", card);
        return g.atBottom || g.headingTop - g.stuck < 40;
      }, { timeout: 8000, message: `load ${load}` })
      .toBe(true);
    const g = await layout(page, "onboarding-email", card);
    expect(g.headingTop, `load ${load}: ${JSON.stringify(g)}`).toBeGreaterThanOrEqual(g.stuck - 1);
    expect(g.headingTop, `load ${load}: ${JSON.stringify(g)}`).toBeLessThan(664);
    await ctx.close();
  }
});
