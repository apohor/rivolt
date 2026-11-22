import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Journey 1 of the planner journeys (see the PR): a person signed in with the sample truck goes from Now to a planned trip on a phone, pressing every
// button on the way and going back after each change of mind. The checks name what a person would see; each step leaves a still in
// e2e/__screenshots__ (the CI artifact). Stubs carry the server's shapes: the plan is the demo plan, favorites are the list the server keeps
// (a PUT replaces it), the saved-trip POST answers with the saved trip.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
test.use({ actionTimeout: 5000 });
test.setTimeout(60_000);

const j = (b: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(b) });
type Fav = { id: string; label: string; latitude: number; longitude: number };

async function shot(page: Page, theme: string, name: string) {
  await page.screenshot({ path: path.join(SHOTS, `journey-plan-truck-${name}-${theme}.png`) });
}

for (const theme of ["dark", "light"] as const) {
  test(`phone ${theme}: with the sample truck, from Now to a planned trip, every button, back included (key on)`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
    await stubApi(page, { designPreview: ["plan_v2"] });
    let favs: Fav[] = [{ id: "f1", label: "Sample destination", latitude: 36.17, longitude: -115.14 }];
    const puts: Fav[][] = [];
    await page.route("**/api/settings/planner/favorites", (r) => {
      if (r.request().method() === "PUT") { favs = r.request().postDataJSON() as Fav[]; puts.push(favs); }
      return r.fulfill(j(favs));
    });
    const planReqs: Record<string, unknown>[] = [];
    await page.route("**/api/trips/plan", (r) => { planReqs.push(r.request().postDataJSON()); return r.fulfill(j(DEMO_PLAN)); });
    const saves: Record<string, unknown>[] = [];
    await page.route("**/api/trips/saved", (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      const body = r.request().postDataJSON() as Record<string, unknown>;
      saves.push(body);
      return r.fulfill(j({ id: "st-new", name: body.name, inputs: body.inputs, plan: body.plan, created_at: "2026-11-22T12:00:00Z", updated_at: "2026-11-22T12:00:00Z" }, 201));
    });

    await test.step("Now, then the Plan tab of the bottom bar", async () => {
      await page.goto("/");
      await expect(page.getByRole("link", { name: "Plan" })).toBeVisible();
      await page.getByRole("link", { name: "Plan" }).click();
      await expect(page).toHaveURL(/\/trips\/plan/);
      await expect(page.locator("[data-plan-form]")).toBeVisible();
      await shot(page, theme, "01-form");
    });

    await test.step("the three tabs, each one back to the planner", async () => {
      await page.getByRole("tab", { name: /^Saved/ }).click();
      await expect(page.getByRole("button", { name: /Sample trip 1/ })).toBeVisible();
      await page.getByRole("tab", { name: "Planner" }).click();
      await expect(page.locator("[data-plan-form]")).toBeVisible();
      await page.getByRole("tab", { name: "Plan vs actual" }).click();
      await expect(page.locator("[data-plan-form]")).toHaveCount(0);
      await page.getByRole("tab", { name: "Planner" }).click();
      await expect(page.locator("[data-plan-form]")).toBeVisible();
    });

    const from = page.locator("[data-plan-field][data-heading='From']");
    const to = page.locator("[data-plan-field][data-heading='To']");
    await test.step("From and To: the chips, clear, swap and swap back", async () => {
      await from.getByRole("button", { name: /Home/ }).click();
      await expect(page.getByRole("button", { name: "Clear From" })).toBeVisible();
      await page.getByRole("button", { name: "Clear From" }).click();
      await expect(page.getByRole("button", { name: "Clear From" })).toHaveCount(0);
      await from.getByRole("button", { name: /Home/ }).click();
      await to.getByRole("button", { name: "Sample destination" }).click();
      await page.getByRole("button", { name: "Clear To" }).click();
      await expect(page.getByRole("button", { name: "Clear To" })).toHaveCount(0);
      await to.getByRole("button", { name: "Sample destination" }).click();
      await page.getByRole("button", { name: "Swap From and To" }).click();
      await expect(from).toContainText("Sample destination");
      await expect(to).toContainText("Home");
      await page.getByRole("button", { name: "Swap From and To" }).click();
      await expect(from).toContainText("Home");
      await expect(to).toContainText("Sample destination");
      await shot(page, theme, "02-from-to");
    });

    await test.step("an overnight, then cancel it", async () => {
      await page.getByRole("button", { name: "+ Add an overnight" }).click();
      await expect(page.getByText("Where will you stop for the night?")).toBeVisible();
      await page.getByRole("button", { name: "cancel" }).click();
      await expect(page.getByText("Where will you stop for the night?")).toHaveCount(0);
    });

    await test.step("departure: each chip, the date and the time, then back to Now", async () => {
      await page.getByRole("button", { name: "+1h" }).click();
      await page.getByRole("button", { name: "Tomorrow 8am" }).click();
      const exact = page.locator("[data-plan-when-exact] button");
      await exact.nth(0).click();
      await expect(page.getByText("Today", { exact: true })).toBeVisible();
      await page.getByRole("button", { name: "Today", exact: true }).click();
      await exact.nth(1).click();
      await expect(page.getByText("Hour", { exact: true })).toBeVisible();
      await exact.nth(1).click();
      await page.getByRole("button", { name: "Now" }).click();
      await shot(page, theme, "03-departure");
    });

    await test.step("starting charge and the more options, opened and closed", async () => {
      await page.getByRole("slider", { name: "Starting SoC" }).fill("85");
      await page.getByRole("button", { name: "Use live" }).click();
      await page.getByRole("slider", { name: "Starting SoC" }).fill("85");
      const more = page.getByRole("button", { name: "More options" });
      await more.click();
      await expect(page.getByLabel("Target arrival SoC %")).toBeVisible();
      await page.getByRole("button", { name: "+ add via stop" }).click();
      await expect(page.getByPlaceholder("Search city or place…")).toBeVisible();
      await page.getByRole("button", { name: "cancel" }).click();
      await expect(page.getByPlaceholder("Search city or place…")).toHaveCount(0);
      await page.getByRole("radio", { name: "Conserve" }).check();
      await expect(page.getByRole("radio", { name: "Conserve" })).toBeChecked();
      await page.getByRole("radio", { name: "All-Purpose" }).check();
      await page.getByRole("switch", { name: "No" }).click();
      await expect(page.getByRole("switch", { name: "Yes" })).toBeVisible();
      await page.getByRole("switch", { name: "Yes" }).click();
      for (const label of ["Tolls", "Ferries", "Highways"]) {
        await page.getByLabel(label).check();
        await expect(page.getByLabel(label)).toBeChecked();
        await page.getByLabel(label).uncheck();
      }
      await page.getByLabel("Target arrival SoC %").fill("15");
      await shot(page, theme, "04-more-options");
      await more.click();
      await expect(page.getByLabel("Target arrival SoC %")).toBeHidden();
    });

    await test.step("Plan trip, and the result", async () => {
      await page.getByRole("button", { name: "Plan trip" }).click();
      await expect(page.locator("[data-plan-bar]")).toBeVisible();
      expect(planReqs.length).toBe(1);
      await shot(page, theme, "05-result");
    });

    await test.step("the result: details open and shut, charger chips, save, edit and update, back to the form", async () => {
      const bar = page.locator("[data-plan-bar]");
      const details = page.getByRole("button", { name: /Trip details/ });
      if (await details.count()) {
        await details.first().click();
        await details.first().click();
      }
      await shot(page, theme, "05b-details-open");
      await page.getByRole("button", { name: "Map", exact: true }).click();
      await expect(page.locator("button[aria-pressed]").filter({ hasText: /^\s*(✓\s*)?Fast\s*$/ })).toBeVisible();
      await shot(page, theme, "05c-map-with-chips");
      for (const name of ["Fast", "Medium", "Hotel"]) {
        const chip = page.locator("button[aria-pressed]").filter({ hasText: new RegExp(`^\\s*(✓\\s*)?${name}\\s*$`) });
        await expect(chip).toBeVisible();
        const before = await chip.getAttribute("aria-pressed");
        await chip.click();
        await expect(chip).not.toHaveAttribute("aria-pressed", before ?? "");
        await chip.click();
        await expect(chip).toHaveAttribute("aria-pressed", before ?? "");
      }
      await page.getByRole("button", { name: /Trip details/ }).first().click();
      const saveBar = page.locator("[data-save-trip]").first();
      await saveBar.getByRole("button", { name: "Save trip" }).click();
      await saveBar.getByRole("button", { name: "Cancel" }).click();
      expect(saves.length).toBe(0);
      await saveBar.getByRole("button", { name: "Save trip" }).click();
      await saveBar.getByRole("button", { name: "Save", exact: true }).click();
      await expect.poll(() => saves.length).toBe(1);
      await shot(page, theme, "06-saved");
      await bar.getByRole("button").first().click();
      await expect(page.locator("[data-plan-form]")).toBeVisible();
      await page.getByRole("button", { name: "Swap From and To" }).click();
      await page.getByRole("button", { name: "Swap From and To" }).click();
      await shot(page, theme, "07-edit");
      await page.getByRole("button", { name: /Update plan/ }).click();
      await expect.poll(() => planReqs.length).toBe(2);
      await expect(bar).toBeVisible();
    });

    await ctx.close();
  });
}
