import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Units live on the account (GET and PATCH /api/settings/units, and me.units), so the web and the iPhone app agree. This browser
// keeps a copy for the signed-out pages and the demo, and follows the account when signed in.
type Units = { distance_unit: "mi" | "km"; temperature_unit: "c" | "f"; distance_source: "user" | "region" | "unset"; temperature_source: "user" | "follows_distance" };
const UNSET: Units = { distance_unit: "mi", temperature_unit: "f", distance_source: "unset", temperature_source: "follows_distance" };

async function open(page: Page, units: Units) {
  const patches: unknown[] = [];
  await stubApi(page);
  let account = units;
  await page.route("**/api/settings/units", async (route) => {
    if (route.request().method() === "PATCH") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      patches.push(body);
      if (body.seed || body.distance_unit) {
        const d = (body.distance_unit as Units["distance_unit"]) ?? account.distance_unit;
        account = { ...account, distance_unit: d, distance_source: body.seed ? "region" : "user", temperature_unit: account.temperature_source === "user" ? account.temperature_unit : d === "km" ? "c" : "f" };
      }
      if (body.temperature_unit) {
        const t = body.temperature_unit as string;
        account = t === "auto" ? { ...account, temperature_source: "follows_distance" } : { ...account, temperature_unit: t as Units["temperature_unit"], temperature_source: "user" };
      }
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(account) });
  });
  return patches;
}

const stored = (page: Page) => page.evaluate(() => JSON.parse(localStorage.getItem("rivolt.preferences.v1") ?? "null"));
const activeDistance = (page: Page) => page.getByRole("button", { name: /^(Miles|Kilometers)/ }).evaluateAll((els) => els.filter((e) => /emerald/.test(e.className)).map((e) => e.textContent));

test("a new account is seeded once from the browser's region", async ({ browser }) => {
  const ctx = await browser.newContext({ locale: "de-DE" });
  const page = await ctx.newPage();
  const patches = await open(page, UNSET);
  await page.goto("/settings#display");
  await expect.poll(() => patches.length).toBe(1);
  expect(patches[0]).toEqual({ seed: true, distance_unit: "km" });
  await expect.poll(() => activeDistance(page)).toEqual(["Kilometers (km)"]);
  await ctx.close();
});

test("an account with units wins over a different local choice", async ({ browser }) => {
  const ctx = await browser.newContext({ locale: "en-US" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: "mi", temperatureUnit: "f", temperatureUnitSet: true })));
  const patches = await open(page, { distance_unit: "km", temperature_unit: "c", distance_source: "user", temperature_source: "user" });
  await page.goto("/settings#display");
  await expect.poll(() => activeDistance(page)).toEqual(["Kilometers (km)"]);
  expect(patches).toEqual([]);
  expect(await stored(page)).toMatchObject({ distanceUnit: "km", temperatureUnit: "c" });
  await ctx.close();
});

test("an unset account takes this browser's saved choice once", async ({ browser }) => {
  const ctx = await browser.newContext({ locale: "en-US" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: "km", temperatureUnit: "c", temperatureUnitSet: false })));
  const patches = await open(page, UNSET);
  await page.goto("/settings#display");
  await expect.poll(() => patches.length).toBe(1);
  expect(patches[0]).toEqual({ distance_unit: "km" });
  await ctx.close();
});

test("a pick in Settings is saved to the account", async ({ browser }) => {
  const ctx = await browser.newContext({ locale: "en-US" });
  const page = await ctx.newPage();
  const patches = await open(page, { distance_unit: "mi", temperature_unit: "f", distance_source: "user", temperature_source: "follows_distance" });
  await page.goto("/settings#display");
  await expect.poll(() => activeDistance(page)).toEqual(["Miles (mi)"]);
  await page.getByRole("button", { name: "Celsius (°C)" }).click();
  await expect.poll(() => patches.length).toBe(1);
  expect(patches[0]).toEqual({ temperature_unit: "c" });
  await page.getByRole("button", { name: "Kilometers (km)" }).click();
  await expect.poll(() => patches.length).toBe(2);
  expect(patches[1]).toEqual({ distance_unit: "km" });
  await ctx.close();
});
