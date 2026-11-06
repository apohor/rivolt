import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// qa's planner baseline (phone): the From/To rows ran about 40 px past their card, a failed plan showed its error far below the Plan button, and
// "no truck linked" told people to enter a starting charge.
async function fill(page: Page) {
  await stubApi(page);
  await page.route("**/api/geocode*", (r) => {
    const q = new URL(r.request().url()).searchParams.get("q") ?? "";
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ name: `${q} with a rather long name`, admin1: "Nebraska", country: "United States", latitude: 41.26, longitude: -95.9 }]) });
  });
  await page.goto("/trips/plan");
  const bar = page.locator("[data-plan-bar]");
  await bar.locator("[data-plan-field][data-heading='From']").getByRole("button", { name: /Home/ }).click();
  const to = bar.locator("[data-plan-field][data-heading='To']");
  await to.getByPlaceholder(/To/).fill("Omaha");
  await page.getByRole("button", { name: /Omaha/ }).first().click();
  return bar;
}

test("phone: the From and To rows stay inside their cards", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const bar = await fill(page);
  await expect(bar.locator("[data-plan-field][data-heading='To']").getByRole("button", { name: /^Clear To/ })).toBeVisible();
  const bad = await page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>("[data-plan-field]")].flatMap((f) => {
      const r = f.getBoundingClientRect();
      return [...f.querySelectorAll<HTMLElement>("*")].filter((c) => c.getBoundingClientRect().right > r.right + 1).map((c) => `${f.getAttribute("data-heading")}: ${c.tagName} ${c.textContent?.slice(0, 20)}`);
    }),
  );
  expect(bad, "children past their card's right edge").toEqual([]);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await ctx.close();
});

test("phone: with no truck linked the error sits right under the Plan button and says to connect Rivian", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const bar = await fill(page);
  await page.route("**/api/trips/plan", (r) => r.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ error: "vehicle_id required (no vehicle linked to this user)" }) }));
  const button = bar.getByRole("button", { name: "Plan trip" });
  await button.click();
  const error = page.getByText("Connect your Rivian to plan a trip.");
  await expect(error).toBeVisible();
  await expect(page.getByText(/enter a starting charge/i)).toHaveCount(0);
  const b = (await button.boundingBox())!;
  const e = (await error.boundingBox())!;
  expect(e.y - (b.y + b.height), "the error starts within 80 px under the button").toBeLessThan(80);
  expect(e.y).toBeGreaterThanOrEqual(b.y);
  await ctx.close();
});
