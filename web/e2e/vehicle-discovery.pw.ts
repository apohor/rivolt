import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// A linked Rivian account with no vehicle on file says why, from GET /api/vehicles/discovery: looking (a 503 too), none, or Rivian needs a new sign-in
// (that one first). Where the server has no such answer (404) the older wording stays.
const j = (status: number, b: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(b) });

const CASES = [
  { name: "looking", answer: () => j(200, { state: "looking" }), title: "Looking for your vehicles", body: /checking your Rivian account/, button: null },
  { name: "looking on a 503", answer: () => j(503, { error: "unavailable" }), title: "Looking for your vehicles", body: /checking your Rivian account/, button: null },
  { name: "none", answer: () => j(200, { state: "none" }), title: "No vehicles on this Rivian account yet", body: /looks again about once a day/, button: "Open Rivian settings" },
  { name: "reauth", answer: () => j(200, { state: "reauth" }), title: "Reconnect your Rivian", body: /sign in again/, button: "Reconnect Rivian" },
  { name: "no word (404)", answer: () => j(404, { error: "not found" }), title: "Connected, but no vehicles found", body: /doesn't list a vehicle yet/, button: "Fix in Settings" },
] as const;

for (const c of CASES) {
  for (const w of [390, 1280] as const) {
    test(`@webkit no vehicle yet: ${c.name} · ${w}px`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: "dark" });
      const page = await ctx.newPage();
      await stubApi(page, { rivian: "ok" });
      await page.route("**/api/vehicles/owned", (r) => r.fulfill(j(200, { vehicles: [] })));
      await page.route("**/api/vehicles/discovery", (r) => r.fulfill(c.answer()));
      await page.goto("/drives");
      const card = page.getByRole("heading", { name: c.title });
      await expect(card).toBeVisible();
      await expect(page.getByText(c.body).first()).toBeVisible();
      if (c.button) await expect(page.getByRole("link", { name: c.button })).toHaveAttribute("href", "/settings?tab=account#rivian");
      else await expect(page.getByRole("link", { name: /Reconnect Rivian|Open Rivian settings|Fix in Settings/ })).toHaveCount(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).include("[data-discovery]").analyze();
      expect(ax.violations.map((x) => x.id)).toEqual([]);
      await ctx.close();
    });
  }
}
