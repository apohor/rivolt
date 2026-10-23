import { expect, test } from "@playwright/test";
import { e2eNow } from "./clock";
import { stubApi } from "./stub";

// qa2's demo audit (new look): the Now cost tile is the period's charging spend, the same number
// Money shows; the Charging locations percents are the kWh shares and add up to 100.
for (const w of [390, 1280] as const) {
  test(`Now charging cost equals Money's total · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    await page.addInitScript(() => {
      localStorage.setItem("rivolt.period", JSON.stringify({ kind: "month", offset: 0 }));
    });
    await stubApi(page, { scenario: "parked" });
    await page.goto("/");
    const tile = page.locator("[data-ui-area='kpi']").getByText("Charging cost", { exact: true }).locator("xpath=..");
    await expect(tile).toBeVisible();
    await expect(tile.locator(".text-xl")).toHaveText(/^\$[\d,]+\.\d{2}$/);
    const now = (await tile.locator(".text-xl").innerText()).trim();
    await page.goto("/money");
    const money = (await page.locator("[data-money-headline]").innerText()).trim();
    expect(now).toBe(money);
    await ctx.close();
  });
}

test("Charging locations percents are the kWh shares and add up to 100", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
  });
  await stubApi(page, { scenario: "parked" });
  // One cluster per bucket so the Charging locations card renders (the stub has none).
  await page.route("**/api/charges/clusters", (r) =>
    r.fulfill({ json: [
      { label: "Home", lat: 0, lon: 0, sessions: 1, energy_kwh: 40, radius_m: 50, member_ids: ["charge-1"] },
      { label: "Public", lat: 0, lon: 0, sessions: 1, energy_kwh: 40, radius_m: 50, member_ids: ["charge-2"] },
      { label: "Fast", lat: 0, lon: 0, sessions: 1, energy_kwh: 40, radius_m: 50, member_ids: ["charge-3"] },
    ] }),
  );
  await page.goto("/");
  const cards = page.getByText("Charging locations").locator("xpath=ancestor::section[1]");
  await expect(cards).toBeVisible();
  const res = await cards.evaluate((el) => {
    const tiles = [...el.querySelectorAll("[data-loc-share]")].map((s) => {
      const box = s.closest("div.rounded-xl")!;
      const pct = Number((s.textContent || "").replace(/[^0-9.]/g, ""));
      const kwh = Number(((box.textContent || "").match(/([\d.]+) kWh added/) || [])[1]);
      return { pct, kwh };
    });
    return tiles;
  });
  expect(res.length).toBe(3);
  expect(res.reduce((a, r) => a + r.pct, 0)).toBe(100);
  const total = res.reduce((a, r) => a + r.kwh, 0);
  for (const r of res) expect(Math.abs(r.pct - (r.kwh / total) * 100)).toBeLessThanOrEqual(1);
});

test("All time: the miles chart spans the recorded data, not a 30-day window", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
  });
  await stubApi(page, { scenario: "parked" });
  await page.goto("/");
  const chart = page.locator("svg[aria-label='Miles per day']");
  await expect(chart).toBeVisible();
  const count = () => chart.locator("path[data-bar]").count();
  // The stub's drives are a few days old: roughly a week of bars (7 minimum), never the 30-day window
  // (30 is also what shows for a moment before the drives load, so wait for it to settle).
  await expect.poll(count).toBeLessThan(20);
  expect(await count()).toBeGreaterThanOrEqual(7);
});

test("Money compares a month in progress with the same days of the last month", async ({ page }) => {
  await stubApi(page, { scenario: "parked" });
  // Mid-month of the fixtures' month (set after stubApi so a pinned run keeps it):
  // on the 1st there's no earlier spend to compare "Oct 1–1" with, so the app rightly
  // shows no comparison, and the nightly run at 00:15 UTC would land there.
  const d = new Date(e2eNow());
  await page.clock.setFixedTime(new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 15, 12)));
  await page.goto("/money");
  const hero = page.getByRole("region", { name: "Statement" });
  await expect(hero).toBeVisible();
  const day = await page.evaluate(() => new Date().getDate());
  await expect(hero).toContainText(new RegExp(`vs [A-Za-z]+ 1–${day}(?!\\d)`));
});

