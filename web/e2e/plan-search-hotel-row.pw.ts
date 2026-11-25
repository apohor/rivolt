import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// A hotel the server took from the charger list comes first in the place search and says "EV charging" under its name.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const w of [390, 1280] as const) {
  test(`place search row shows the hotel with its EV charging note · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    let seen = "";
    await page.route("**/api/geocode*", (r) => {
      seen = r.request().url();
      return r.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          { name: "Hampton Inn Dalhart", latitude: 36.06, longitude: -102.51, country: "United States", country_code: "us", admin1: "TX", locality: "Dalhart", ev_charging: true },
          { name: "Hampton Inn, Amarillo", latitude: 35.2, longitude: -101.9, country: "United States", admin1: "Texas", locality: "Amarillo" },
        ]),
      });
    });
    await page.goto("/trips/plan");
    await page.getByPlaceholder("To — city or address").fill("hampton inn dalhart");
    const first = page.getByRole("button", { name: /Hampton Inn Dalhart/ }).first();
    await expect(first).toContainText("TX, United States · EV charging");
    await expect(page.getByRole("button", { name: /Hampton Inn, Amarillo/ })).not.toContainText("EV charging");
    expect(seen).toContain("q=hampton%20inn%20dalhart");
    await page.screenshot({ path: path.join(SHOTS, `plan-search-hotel-row-${w}.png`) });
    // With a start chosen near Dalhart, the next search carries that point (rounded to two decimals).
    await page.getByPlaceholder("To — city or address").fill("");
    await page.getByPlaceholder("From — city or address").fill("hampton inn dalhart");
    await page.getByRole("button", { name: /Hampton Inn Dalhart/ }).first().click();
    await page.getByPlaceholder("To — city or address").fill("hampton inn");
    await expect(page.locator("ul button", { hasText: "Hampton Inn Dalhart" })).toContainText("EV charging");
    expect(seen).toContain("q=hampton%20inn&");
    expect(seen).toContain("lat=36.06&lon=-102.51");
    await page.screenshot({ path: path.join(SHOTS, `plan-search-hotel-row-near-${w}.png`) });
    await ctx.close();
  });
}
