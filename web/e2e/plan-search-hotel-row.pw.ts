import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// A hotel the server took from the charger list comes first in the place search and says "EV charging" under its name.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const scheme of ["light", "dark"] as const)
for (const w of [390, 1280] as const) {
  test(`place search row shows the hotel with its EV charging note · ${scheme} ${w}px @webkit`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: scheme });
    const page = await ctx.newPage();
    await stubApi(page);
    let seen = "";
    await page.route("**/api/geocode*", (r) => {
      seen = r.request().url();
      return r.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          { name: "Super 8, Dalhart", latitude: 36.06, longitude: -102.51, country: "United States", country_code: "us", admin1: "Texas", locality: "Dalhart", ev_charging: true },
          { name: "Hampton Inn, Amarillo", latitude: 35.2, longitude: -101.9, country: "United States", admin1: "Texas", locality: "Amarillo" },
        ]),
      });
    });
    await page.goto("/trips/plan");
    await page.getByPlaceholder("To — city or address").fill("super 8 dalhart");
    const first = page.getByRole("button", { name: /Super 8, Dalhart/ }).first();
    await expect(first).toContainText("Texas, United States · EV charging");
    await expect(page.getByRole("button", { name: /Hampton Inn, Amarillo/ })).not.toContainText("EV charging");
    expect(seen).toContain("q=super%208%20dalhart");
    await page.screenshot({ path: path.join(SHOTS, `plan-search-hotel-row-${scheme}-${w}.png`) });
    // With a start chosen near Dalhart, the next search carries that point (rounded to two decimals).
    await page.getByPlaceholder("To — city or address").fill("");
    await page.getByPlaceholder("From — city or address").fill("super 8 dalhart");
    await page.getByRole("button", { name: /Super 8, Dalhart/ }).first().click();
    await page.getByPlaceholder("To — city or address").fill("hampton inn");
    await expect(page.locator("ul button", { hasText: "Super 8, Dalhart" })).toContainText("EV charging");
    expect(seen).toContain("q=hampton%20inn&");
    expect(seen).toContain("lat=36.06&lon=-102.51");
    await page.screenshot({ path: path.join(SHOTS, `plan-search-hotel-row-near-${scheme}-${w}.png`) });
    await ctx.close();
  });
}

// The box asks once, after a pause, for three or more characters; a 429 says "Too many searches", not "unavailable".
test("the place search box waits for a pause and three characters, and names a 429 @webkit", async ({ page }) => {
  await stubApi(page);
  const asked: string[] = [];
  let tooMany = false;
  await page.route("**/api/geocode*", (r) => {
    asked.push(new URL(r.request().url()).searchParams.get("q") ?? "");
    if (tooMany) return r.fulfill({ status: 429, contentType: "application/json", body: JSON.stringify({ error: "slow down" }) });
    return r.fulfill({ json: [{ name: "Denver", latitude: 39.7, longitude: -105, country: "United States", admin1: "Colorado" }] });
  });
  await page.goto("/trips/plan");
  const to = page.getByPlaceholder("To — city or address");
  await to.pressSequentially("de", { delay: 20 });
  await page.waitForTimeout(700);
  expect(asked, "two characters send nothing").toEqual([]);
  await to.pressSequentially("nver", { delay: 20 });
  await expect(page.getByRole("button", { name: /Denver/ })).toBeVisible();
  expect(asked, "typed in one burst: one call for the finished word").toEqual(["denver"]);
  tooMany = true;
  await to.fill("");
  await to.fill("boise");
  await expect(page.getByText("Too many searches, wait a moment")).toBeVisible();
  await page.waitForTimeout(1500);
  expect(asked.filter((q) => q === "boise"), "a 429 is not retried").toHaveLength(1);
});
