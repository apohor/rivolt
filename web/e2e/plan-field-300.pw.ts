import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// #1991: at Safari's largest text (aA 300 %: 131 x 232 CSS px) the From and To rows' actions ("☆ save place", "clear") would not shrink or wrap,
// so the row ran to 177 px and the whole page scrolled sideways. They wrap under the place now, inside the screen.
test("@webkit phone at aA 300%: the From and To rows fit the screen, actions and all", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 131, height: 232 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.route("**/api/geocode*", (r) => r.fulfill({ json: [{ name: new URL(r.request().url()).searchParams.get("q") ?? "", admin1: "US", country: "", latitude: 41.26, longitude: -95 }] }));
  await page.goto("/trips/plan");
  const bar = page.locator("[data-plan-endpoints]");
  // From is Home (no "save place" for it); To is a searched place, with both actions.
  await bar.locator("[data-plan-field][data-heading='From']").getByRole("button", { name: /Home/ }).click();
  await bar.locator("[data-plan-field][data-heading='To']").getByPlaceholder(/To/).fill("Omaha");
  await page.getByRole("button", { name: /Omaha/ }).first().click();
  const to = bar.locator("[data-plan-field][data-heading='To']");
  await expect(to.getByRole("button", { name: "Clear To" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(131);
  for (const name of [/Save place: Omaha/, /Clear To/, /Clear From/]) {
    const b = (await bar.getByRole("button", { name }).boundingBox())!;
    expect(b.x, String(name)).toBeGreaterThanOrEqual(0);
    expect(b.x + b.width, String(name)).toBeLessThanOrEqual(131);
  }
  await to.screenshot({ path: test.info().outputPath("field-300-to.png") });
  await ctx.close();
});
