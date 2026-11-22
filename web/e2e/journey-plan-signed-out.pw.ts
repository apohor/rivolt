import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Journey 3 of the planner journeys: a person who is signed out reaches the planner from the landing page on a phone, plays with the sample
// trips of the demo, and presses every button on the way, going back after each change of mind. Stubs carry the server's shapes: /api/auth/me
// answers 401, the landing and the demo are on, the demo serves its two preset trips and their plans, the saved list is the sample list.
// Each step leaves a still in e2e/__screenshots__ (the CI artifact).
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
test.use({ actionTimeout: 5000 });
test.setTimeout(60_000);

async function shot(page: Page, theme: string, name: string) {
  await page.screenshot({ path: path.join(SHOTS, `journey-plan-signed-out-${name}-${theme}.png`) });
}

for (const theme of ["dark", "light"] as const) {
  test(`phone ${theme}: signed out, from the landing page to a sample trip, every button, back included (key on)`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
    await stubApi(page, { designPreview: ["plan_v2"], signedIn: false, landing: true, demo: true, nowEndpoint: true });

    await test.step("the landing page: Sign in, Try the demo and Connect your Rivian, each one back", async () => {
      await page.goto("/");
      await expect(page.getByRole("heading", { name: /Plan the trip\. Charging included\./ })).toBeVisible();
      await shot(page, theme, "01-landing");
      await page.getByRole("link", { name: "Sign in", exact: true }).click();
      await expect(page).toHaveURL(/\/login/);
      await page.goBack();
      await expect(page.getByRole("heading", { name: /Plan the trip\. Charging included\./ })).toBeVisible();
      await page.getByRole("link", { name: "Try the demo", exact: true }).click();
      await expect(page).toHaveURL(/\/demo$/);
      await page.goBack();
      await expect(page.getByRole("heading", { name: /Plan the trip\. Charging included\./ })).toBeVisible();
      await page.getByRole("link", { name: "Connect your Rivian" }).first().click();
      await expect(page).toHaveURL(/\/signup/);
      await page.goBack();
      await expect(page.getByRole("heading", { name: /Plan the trip\. Charging included\./ })).toBeVisible();
    });

    await test.step("Plan a sample trip: the demo planner with the two sample trips", async () => {
      await page.getByRole("link", { name: "Plan a sample trip" }).first().click();
      await expect(page).toHaveURL(/\/demo\/trips\/plan/);
      await expect(page.getByText("Sample trips planned for the demo truck.")).toBeVisible();
      await expect(page.getByRole("button", { name: /LA to Las Vegas/ })).toBeVisible();
      await expect(page.getByRole("button", { name: /LA to San Diego/ })).toBeVisible();
      await shot(page, theme, "02-sample-trips");
    });

    await test.step("a sample trip, its details open and shut, Map, then another sample trip", async () => {
      await page.getByRole("button", { name: /LA to Las Vegas/ }).click();
      await expect(page.getByRole("button", { name: /LA to Las Vegas/ })).toHaveAttribute("aria-pressed", "true");
      await expect(page.getByRole("button", { name: /LA to San Diego/ })).toHaveAttribute("aria-pressed", "false");
      const details = page.getByRole("button", { name: /^Trip details:/ });
      await expect(details).toBeVisible();
      await expect(page.getByText("arrive at 24")).toBeVisible();
      await shot(page, theme, "03-result");
      await details.click();
      const before = await details.getAttribute("aria-label");
      await details.click();
      expect(await details.getAttribute("aria-label")).not.toBe(before);
      await shot(page, theme, "03b-details");
      // The details sit over the whole screen: Map brings the map back, and a swipe up reaches the sample trips above it.
      await page.getByRole("button", { name: "Map", exact: true }).click();
      await shot(page, theme, "03c-map");
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.getByRole("button", { name: /LA to San Diego/ }).click();
      await expect(page.getByRole("button", { name: /LA to San Diego/ })).toHaveAttribute("aria-pressed", "true");
      await expect(page.getByRole("button", { name: /LA to Las Vegas/ })).toHaveAttribute("aria-pressed", "false");
      await expect(page.getByRole("button", { name: /^Trip details:/ })).toBeVisible();
      await shot(page, theme, "04-second-sample");
    });

    await test.step("Saved: a sample saved trip opens, back to the planner; Plan vs actual and back", async () => {
      await page.getByRole("tab", { name: /^Saved/ }).click();
      await expect(page.getByText("Connect your Rivian to save your own trips.")).toBeVisible();
      await shot(page, theme, "05-saved");
      await page.getByRole("button", { name: /Sample trip 1/ }).click();
      // Opening a saved trip goes to the Planner tab.
      await expect(page.getByRole("tab", { name: "Planner" })).toHaveAttribute("aria-selected", "true");
      await expect(page.getByText("Saved trips")).toHaveCount(0);
      await shot(page, theme, "05b-saved-opened");
      await page.getByRole("tab", { name: "Plan vs actual" }).click();
      await expect(page.getByRole("tab", { name: "Plan vs actual" })).toHaveAttribute("aria-selected", "true");
      await expect(page.getByText("Sample trips planned for the demo truck.")).toHaveCount(0);
      await shot(page, theme, "06-plan-vs-actual");
      await page.getByRole("tab", { name: "Planner" }).click();
      await expect(page.getByText("Sample trips planned for the demo truck.")).toBeVisible();
    });

    await test.step("the connect lines and the bottom bar, each one back", async () => {
      await page.getByRole("link", { name: "Connect your Rivian" }).first().click();
      await expect(page).toHaveURL(/\/signup/);
      await page.goBack();
      await expect(page).toHaveURL(/\/demo\/trips\/plan/);
      await page.getByRole("link", { name: "Connect with a driver account" }).click();
      await expect(page).toHaveURL(/\/signup/);
      await page.goBack();
      await page.getByRole("link", { name: "Now", exact: true }).click();
      await expect(page).toHaveURL(/\/demo$/);
      await page.getByRole("link", { name: "Plan", exact: true }).click();
      await expect(page).toHaveURL(/\/demo\/trips\/plan/);
      await expect(page.getByText("Sample trips planned for the demo truck.")).toBeVisible();
    });

    await ctx.close();
  });
}
