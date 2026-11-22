import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Journey 2 of the planner journeys: a person signed in whose Rivian is not connected (no truck, no saved home, no favorites, no saved trips)
// walks the planner on a phone from Now to a planned trip, pressing every button and going back after each change of mind. Stubs carry the
// server's shapes: no vehicles ([]), the Rivian status unauthenticated, home not set, favorites and saved trips empty; the plan is the demo plan.
// Each step leaves a still in e2e/__screenshots__ (the CI artifact).
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
test.use({ actionTimeout: 5000 });
test.setTimeout(60_000);

const j = (b: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(b) });

async function shot(page: Page, theme: string, name: string) {
  await page.screenshot({ path: path.join(SHOTS, `journey-plan-no-truck-${name}-${theme}.png`) });
}

const PLACES: Record<string, { name: string; latitude: number; longitude: number; country: string; admin1: string }> = {
  Pasadena: { name: "Pasadena", latitude: 34.15, longitude: -118.14, country: "US", admin1: "California" },
  Mammoth: { name: "Mammoth Lakes", latitude: 37.65, longitude: -118.97, country: "US", admin1: "California" },
};

for (const theme of ["dark", "light"] as const) {
  test(`phone ${theme}: signed in without a truck, from Now to a planned trip, every button, back included (key on)`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
    await stubApi(page, { designPreview: ["plan_v2"], rivian: "disconnected", parkedPlace: "unknown" });
    await page.route("**/api/vehicles", (r) => r.fulfill(j([])));
    await page.route("**/api/settings/planner/favorites", (r) => r.fulfill(j([])));
    await page.route("**/api/geocode*", (r) => {
      const q = new URL(r.request().url()).searchParams.get("q") ?? "";
      const hit = Object.keys(PLACES).find((k) => q.toLowerCase().startsWith(k.toLowerCase().slice(0, 4)));
      return r.fulfill(j(hit ? [PLACES[hit]] : []));
    });
    const planReqs: Record<string, unknown>[] = [];
    await page.route("**/api/trips/plan", (r) => { planReqs.push(r.request().postDataJSON()); return r.fulfill(j(DEMO_PLAN)); });
    const saves: Record<string, unknown>[] = [];
    await page.route("**/api/trips/saved", (r) => {
      if (r.request().method() !== "POST") return r.fulfill(j([]));
      const body = r.request().postDataJSON() as Record<string, unknown>;
      saves.push(body);
      return r.fulfill(j({ id: "st-new", name: body.name, inputs: body.inputs, plan: body.plan, created_at: "2026-11-22T12:00:00Z", updated_at: "2026-11-22T12:00:00Z" }, 201));
    });

    const from = page.locator("[data-plan-field][data-heading='From']");
    const to = page.locator("[data-plan-field][data-heading='To']");

    await test.step("Now, then the Plan tab: the form is there, with the offer to connect a Rivian", async () => {
      await page.goto("/");
      await page.getByRole("link", { name: "Plan" }).click();
      await expect(page).toHaveURL(/\/trips\/plan/);
      await expect(page.locator("[data-plan-form]")).toBeVisible();
      await expect(page.getByText("Connect your Rivian to get started")).toBeVisible();
      await expect(from.getByRole("button", { name: /Home/ })).toHaveCount(0);
      await shot(page, theme, "01-form");
    });

    await test.step("Connect Rivian, then back to the planner", async () => {
      await page.getByRole("button", { name: "Connect Rivian" }).or(page.getByRole("link", { name: "Connect Rivian" })).first().click();
      await expect(page).toHaveURL(/\/settings\?tab=account#rivian/);
      await expect(page.getByText("Use a dedicated driver account")).toBeVisible();
      await shot(page, theme, "01b-connect");
      await page.goBack();
      await expect(page).toHaveURL(/\/trips\/plan/);
      await expect(page.locator("[data-plan-form]")).toBeVisible();
    });

    await test.step("the three tabs, each one back to the planner", async () => {
      await page.getByRole("tab", { name: /^Saved/ }).click();
      await expect(page.getByText(/Sample trip/)).toHaveCount(0);
      await shot(page, theme, "02-saved-empty");
      await page.getByRole("tab", { name: "Planner" }).click();
      await expect(page.locator("[data-plan-form]")).toBeVisible();
      await page.getByRole("tab", { name: "Plan vs actual" }).click();
      await expect(page.locator("[data-plan-form]")).toHaveCount(0);
      await shot(page, theme, "03-plan-vs-actual");
      await page.getByRole("tab", { name: "Planner" }).click();
      await expect(page.locator("[data-plan-form]")).toBeVisible();
    });

    await test.step("From and To: type a place, pick it, clear it, pick again, swap and swap back", async () => {
      await from.getByPlaceholder(/city or address/).fill("Pasadena");
      await page.getByRole("button", { name: /Pasadena/ }).first().click();
      await expect(from).toContainText("Pasadena");
      await page.getByRole("button", { name: "Clear From" }).click();
      await expect(page.getByRole("button", { name: "Clear From" })).toHaveCount(0);
      await from.getByPlaceholder(/city or address/).fill("Pasadena");
      await page.getByRole("button", { name: /Pasadena/ }).first().click();
      await to.getByPlaceholder(/city or address/).fill("Mammoth");
      await page.getByRole("button", { name: /Mammoth Lakes/ }).first().click();
      await expect(to).toContainText("Mammoth Lakes");
      await page.getByRole("button", { name: "Swap From and To" }).click();
      await expect(from).toContainText("Mammoth Lakes");
      await expect(to).toContainText("Pasadena");
      await page.getByRole("button", { name: "Swap From and To" }).click();
      await expect(from).toContainText("Pasadena");
      await expect(to).toContainText("Mammoth Lakes");
      await shot(page, theme, "04-from-to");
    });

    await test.step("an overnight, then cancel it", async () => {
      await page.getByRole("button", { name: "+ Add an overnight" }).click();
      await expect(page.getByText("Where will you stop for the night?")).toBeVisible();
      await page.getByRole("button", { name: "cancel" }).click();
      await expect(page.getByText("Where will you stop for the night?")).toHaveCount(0);
    });

    await test.step("departure: each chip, the date and the time, then back to Now", async () => {
      const now = page.getByRole("button", { name: "Now", exact: true });
      const exact = page.locator("[data-plan-when-exact] button");
      const whenText = async () => (await exact.allInnerTexts()).join(" / ");
      await expect(now).toHaveAttribute("aria-pressed", "true");
      expect(await whenText()).toBe("Select date / Time");
      await page.getByRole("button", { name: "+1h", exact: true }).click();
      await expect(now).toHaveAttribute("aria-pressed", "false");
      const inOneHour = await whenText();
      expect(inOneHour).not.toBe("Select date / Time");
      await page.getByRole("button", { name: "Tomorrow 8am", exact: true }).click();
      const tomorrow = await whenText();
      expect(tomorrow).not.toBe(inOneHour);
      expect(tomorrow).toContain("08:00");
      await exact.nth(0).click();
      await expect(page.getByText("Today", { exact: true })).toBeVisible();
      await page.getByRole("button", { name: "Today", exact: true }).click();
      await exact.nth(1).click();
      await expect(page.getByText("Hour", { exact: true })).toBeVisible();
      await exact.nth(1).click();
      await now.click();
      await expect(now).toHaveAttribute("aria-pressed", "true");
      expect(await whenText()).toBe("Select date / Time");
      await shot(page, theme, "05-departure");
    });

    await test.step("starting charge: with no truck there is no live value, so the slider is the only way", async () => {
      const soc = page.getByRole("slider", { name: "Starting SoC" });
      await expect(soc).toHaveAttribute("aria-valuetext", "auto");
      await soc.fill("70");
      await expect(soc).toHaveValue("70");
      await expect(page.getByRole("status").filter({ hasText: "70%" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Use live" })).toBeVisible();
      await page.getByRole("button", { name: "Use live" }).click();
      await expect(soc).toHaveAttribute("aria-valuetext", "auto");
      await soc.fill("70");
    });

    await test.step("More options, every control, opened and closed", async () => {
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
      await expect(page.getByLabel("Target arrival SoC %")).toHaveValue("15");
      await shot(page, theme, "06-more-options");
      await more.click();
      await expect(page.getByLabel("Target arrival SoC %")).toBeHidden();
    });

    await test.step("Plan trip, and what the server is asked", async () => {
      await page.getByRole("button", { name: "Plan trip" }).click();
      await expect(page.locator("[data-plan-bar]")).toBeVisible();
      expect(planReqs.length).toBe(1);
      expect(planReqs[0]).toMatchObject({ starting_soc: 70, target_arrival_soc_percent: 15, drive_mode: "EVERYDAY", has_adapter: false });
      expect(planReqs[0]).not.toHaveProperty("depart_at");
      const wps = planReqs[0].waypoints as { latitude: number; longitude: number }[];
      expect(wps).toHaveLength(2);
      expect(wps[0]).toMatchObject({ latitude: 34.15, longitude: -118.14 });
      expect(wps[1]).toMatchObject({ latitude: 37.65, longitude: -118.97 });
      await shot(page, theme, "07-result");
    });

    await test.step("the result: details open and shut, Map, save and cancel, save, edit and update", async () => {
      const bar = page.locator("[data-plan-bar]");
      const details = page.getByRole("button", { name: /^Trip details:/ });
      await expect(details).toHaveAttribute("aria-label", /collapse/);
      await details.click();
      await expect(details).toHaveAttribute("aria-label", /expand/);
      await details.click();
      await expect(details).toHaveAttribute("aria-label", /collapse/);
      await page.getByRole("button", { name: "Map", exact: true }).click();
      await shot(page, theme, "07b-map");
      await details.click();
      const saveBar = page.locator("[data-save-trip]").first();
      await saveBar.getByRole("button", { name: "Save trip" }).click();
      await saveBar.getByRole("button", { name: "Cancel" }).click();
      expect(saves.length).toBe(0);
      await saveBar.getByRole("button", { name: "Save trip" }).click();
      await saveBar.getByRole("button", { name: "Save", exact: true }).click();
      await expect.poll(() => saves.length).toBe(1);
      await shot(page, theme, "08-saved");
      await bar.getByRole("button", { name: /Pasadena → Mammoth Lakes/ }).click();
      await expect(page.locator("[data-plan-form]")).toBeVisible();
      await page.getByRole("button", { name: "Swap From and To" }).click();
      await page.getByRole("button", { name: "Swap From and To" }).click();
      await shot(page, theme, "09-edit");
      await page.getByRole("button", { name: /Update plan/ }).click();
      await expect.poll(() => planReqs.length).toBe(2);
      expect(planReqs[1]).toEqual(planReqs[0]);
      await expect(bar).toBeVisible();
    });

    await ctx.close();
  });
}
