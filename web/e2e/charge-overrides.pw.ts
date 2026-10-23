import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// #906 part 2: the user's own place for a charge ("set by you"), pinning it where
// the truck was, and an optional location on a network of their own. Synthetic
// positions only.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const LAT = 40.9;
const LON = -100.9;
const charge = (o: Record<string, unknown>) => ({
  ID: "charge-ov", VehicleID: VEHICLE_ID,
  StartedAt: new Date(e2eNow() - 2 * 86_400_000).toISOString(),
  EndedAt: new Date(e2eNow() - 2 * 86_400_000 + 3_600_000).toISOString(),
  StartSoCPct: 40, EndSoCPct: 80, EnergyAddedKWh: 52, MilesAdded: 120, MaxPowerKW: 11, AvgPowerKW: 9.6,
  FinalState: "charging_complete", Lat: LAT, Lon: LON, Source: "live", Currency: "USD", Cost: 0, PricePerKWh: 0,
  kind: "ac", place: "away", ...o,
});
const NETWORKS = [
  { name: "Office garage", price_per_kwh: 0.22, currency: "USD", lat: LAT + 0.0004, lon: LON },
  { name: "Electrify America", price_per_kwh: 0.48, currency: "USD", slug: "ea" },
];

async function open(page: Page, c: Record<string, unknown>) {
  await stubApi(page);
  const puts: { url: string; body: string }[] = [];
  let current = charge(c);
  await page.route("**/api/charges?**", (r) => r.fulfill({ json: [current] }));
  await page.route("**/api/settings/charging/networks", (r) => r.fulfill({ json: NETWORKS }));
  await page.route("**/api/charges/charge-ov/place", (r) => {
    puts.push({ url: r.request().url(), body: r.request().postData() ?? "" });
    const place = JSON.parse(r.request().postData() ?? "{}").place as string | null;
    current = charge({ ...c, PlaceOverride: place ?? "", place: place ?? "away" });
    return r.fulfill({ status: 204, body: "" });
  });
  await page.route("**/api/charges/charge-ov/location", (r) => {
    puts.push({ url: r.request().url(), body: r.request().postData() ?? "" });
    const recorded = JSON.parse(r.request().postData() ?? "{}").recorded as boolean;
    current = charge({ ...c, LocationOverride: recorded ? "recorded" : "" });
    return r.fulfill({ status: 204, body: "" });
  });
  await page.goto("/charges/charge-ov");
  return puts;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  for (const scheme of ["dark", "light"] as const) {
    test(`charge overrides: set by you, pinned where the truck was · ${v.n} ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await open(page, { PlaceOverride: "home", place: "home", place_auto: "away", LocationOverride: "recorded" });
      await expect(page.getByText("Home · set by you")).toBeVisible();
      const place = page.locator("[data-place-override]");
      await expect(place.getByRole("radio", { name: "Home" })).toHaveAttribute("aria-checked", "true");
      await expect(place).toContainText("set by you");
      await expect(place).toContainText("Kept when your home location changes. Auto: Away.");
      await expect(page.locator("[data-pinned-line]")).toHaveText("Showing where the truck was recorded.");
      await expect(page.getByRole("switch", { name: "Pin where the truck was" })).toHaveAttribute("aria-checked", "true");
      // 44 px targets: each place option and the pin row.
      for (const r of await place.getByRole("radio").all()) expect((await r.boundingBox())!.height).toBeGreaterThanOrEqual(44);
      expect((await page.locator("[data-location-override] label").boundingBox())!.height).toBeGreaterThanOrEqual(44);
      // The user's own network within 150 m labels the pin, and its rate is offered (not applied).
      await expect(page.locator("[data-own-network-label]")).toBeVisible();
      await expect(page.locator(".rivolt-charger-label").first()).toHaveText("Office garage");
      await expect(page.locator("[data-near-own-network]")).toContainText("Office garage");
      await expect(page.getByLabel("Price per kWh")).toHaveValue("");
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      expect((await new AxeBuilder({ page }).include("[data-place-override]").include("[data-location-override]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
      await place.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `charge-overrides-session-${v.n}-${scheme}.png`), fullPage: false });
      await page.locator("[data-location-override]").scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `charge-overrides-location-${v.n}-${scheme}.png`), fullPage: false });
      await ctx.close();
    });
  }
}

test("charge overrides: picking a place and the pin writes once each · phone", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const puts = await open(page, {});
  const place = page.locator("[data-place-override]");
  await expect(place.getByRole("radio", { name: "Auto" })).toHaveAttribute("aria-checked", "true");
  await place.getByRole("radio", { name: "Home" }).click();
  await expect(page.getByText("Home · set by you")).toBeVisible();
  await place.getByRole("radio", { name: "Auto" }).click();
  await expect(place.getByRole("radio", { name: "Auto" })).toHaveAttribute("aria-checked", "true");
  // Tapping the row's text flips the switch.
  await page.locator("[data-location-override] label").getByText("Instead of the nearest public charger.").click();
  await expect(page.getByRole("switch", { name: "Pin where the truck was" })).toHaveAttribute("aria-checked", "true");
  await expect(page.locator("[data-location-override]")).toContainText("set by you");
  expect(puts.map((p) => `${new URL(p.url).pathname} ${p.body}`)).toEqual([
    '/api/charges/charge-ov/place {"place":"home"}',
    '/api/charges/charge-ov/place {"place":null}',
    '/api/charges/charge-ov/location {"recorded":true}',
  ]);
  // Prefill fills the form only; nothing is priced until Save.
  await page.locator("[data-near-own-network]").getByRole("button", { name: "Prefill its rate" }).click();
  await expect(page.getByLabel("Price per kWh")).toHaveValue("0.220");
  await ctx.close();
});

// The states between a tap and the server's answer: saving, then (on a failure) the stored
// value back with an inline error.
for (const scheme of ["dark", "light"] as const) {
  test(`charge overrides: saving and failed states · iphone ${scheme}`, async ({ browser }) => {
    // Reduced motion: the switch has no colour transition, so the shot shows its settled colour.
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: scheme, reducedMotion: "reduce" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/charges?**", (r) => r.fulfill({ json: [charge({ place_auto: "away" })] }));
    await page.route("**/api/settings/charging/networks", (r) => r.fulfill({ json: [] }));
    let release: () => void = () => {};
    const held = new Promise<void>((res) => (release = res));
    await page.route("**/api/charges/charge-ov/place", async (r) => {
      await held;
      await r.fulfill({ status: 500, body: "boom" });
    });
    await page.route("**/api/charges/charge-ov/location", async (r) => {
      await held;
      await r.fulfill({ status: 500, body: "boom" });
    });
    await page.goto("/charges/charge-ov");
    const place = page.locator("[data-place-override]");
    await place.getByRole("radio", { name: "Home" }).click();
    await page.getByRole("switch", { name: "Pin where the truck was" }).click();
    await expect(place).toContainText("Saving…");
    await expect(page.locator("[data-location-override]")).toContainText("Saving…");
    await place.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `charge-overrides-saving-iphone-${scheme}.png`), fullPage: false });
    release();
    // Plain words, never the raw error ("boom").
    await expect(place.getByRole("alert")).toHaveText("Couldn't save. Try again in a moment.");
    await expect(page.locator("[data-location-override]").getByRole("alert")).toHaveText("Couldn't save. Try again in a moment.");
    await expect(page.getByText("boom")).toHaveCount(0);
    await expect(place.getByRole("radio", { name: "Auto" })).toHaveAttribute("aria-checked", "true");
    await expect(page.getByRole("switch", { name: "Pin where the truck was" })).toHaveAttribute("aria-checked", "false");
    // The off track settles to the neutral colour (not green).
    await expect(page.getByRole("switch", { name: "Pin where the truck was" })).not.toHaveClass(/bg-emerald/);
    expect(await page.getByRole("switch", { name: "Pin where the truck was" }).evaluate((el) => getComputedStyle(el).transitionProperty)).toBe("none");
    await page.screenshot({ path: path.join(SHOTS, `charge-overrides-failed-iphone-${scheme}.png`), fullPage: false });
    await ctx.close();
  });
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`settings: a location on a network of your own · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    let saved: unknown = null;
    await page.route("**/api/settings/charging/networks", (r) => {
      if (r.request().method() === "PUT") {
        saved = JSON.parse(r.request().postData() ?? "null");
        return r.fulfill({ json: saved });
      }
      return r.fulfill({ json: NETWORKS });
    });
    await page.goto("/settings#networks");
    const fields = page.locator("[data-network-location]");
    await expect(fields).toHaveCount(1); // the built-in network has none
    const input = fields.getByRole("textbox");
    await expect(input).toHaveValue(`${LAT + 0.0004}, ${LON}`);
    await input.fill("91, 0");
    await expect(fields.getByRole("alert")).toHaveText("Latitude must be between -90 and 90.");
    await expect(input).toHaveAttribute("aria-invalid", "true");
    await expect(fields).toContainText("Only used to label your own charges near it.");
    await fields.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `charge-overrides-network-invalid-${v.n}-dark.png`), fullPage: false });
    await input.fill("40.5, -100.25");
    await expect(fields).toContainText("Only used to label your own charges near it.");
    // Clearing the field removes the location.
    await input.fill("");
    await page.locator("#networks").getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(() => saved).not.toBeNull();
    expect((saved as { name: string; lat?: number }[]).find((r) => r.name === "Office garage")?.lat).toBeUndefined();
    saved = null;
    await input.fill("40.5, -100.25");
    await page.locator("#networks").getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(() => saved).not.toBeNull();
    const rows = saved as { name: string; lat?: number; lon?: number; locDraft?: string }[];
    expect(rows.find((r) => r.name === "Office garage")).toMatchObject({ lat: 40.5, lon: -100.25 });
    expect(rows.every((r) => r.locDraft === undefined)).toBe(true);
    expect(rows.find((r) => r.name === "Electrify America")?.lat).toBeUndefined();
    await page.screenshot({ path: path.join(SHOTS, `charge-overrides-network-${v.n}-dark.png`), fullPage: false });
    await ctx.close();
  });
}
