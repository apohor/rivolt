import { expect, test, type Page } from "@playwright/test";

// The release-candidate smoke (.github/workflows/rc-smoke.yml): the signed-out demo on the deployed preview, on a phone, light and dark.
// Runs only with E2E_LIVE_URL (playwright.config.ts, stub.ts): there is no stub, so the pages meet the real backend. The other specs assert
// the stub's fixtures and cannot hold against real data; this one asserts only that the pages load and say nothing wrong. Never signs in.
test.skip(!process.env.E2E_LIVE_URL, "live smoke: set E2E_LIVE_URL");

const PHONE = { width: 393, height: 852 };

function watch(page: Page) {
  const bad: string[] = [];
  page.on("console", (m) => {
    // Basemap tiles: a range request past the archive's end and the like are not the app's fault.
    if (m.type() === "error" && !/tiles|pmtiles|Bad response code|Failed to load resource.*(404|416)/i.test(m.text())) bad.push(`console: ${m.text().slice(0, 140)}`);
  });
  page.on("pageerror", (e) => {
    if (!/due to access control checks/.test(e.message)) bad.push(`pageerror: ${e.message.slice(0, 140)}`);
  });
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/") && !u.pathname.startsWith("/api/maps/") && r.status() >= 400) bad.push(`${r.status()}: ${u.pathname}`);
  });
  return bad;
}

const PAGES = [
  { name: "Now", url: "/demo", text: /Sample truck\./ },
  { name: "Activity", url: "/demo/activity", heading: "Activity" },
  { name: "Money", url: "/demo/money", heading: "Money" },
  { name: "Plan", url: "/demo/trips/plan", text: /Sample trips planned for the demo truck\./ },
];

for (const theme of ["light", "dark"] as const) {
  test.describe(`${theme}`, () => {
    test.use({ viewport: PHONE, colorScheme: theme });

    for (const p of PAGES) {
      test(`demo ${p.name} loads, no console errors or failed API calls`, async ({ page }) => {
        await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
        const bad = watch(page);
        await page.goto(p.url);
        // A phone drops the pages' subtitle line; the heading says where we are.
        const seen = "heading" in p ? page.getByRole("heading", { name: p.heading }).first() : page.getByText(p.text).first();
        await expect(seen).toBeVisible({ timeout: 20_000 });
        await page.waitForTimeout(1500);
        expect(bad, bad.join("\n")).toEqual([]);
      });
    }

    test("demo planner: a preset opens a sample plan", async ({ page }) => {
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      const bad = watch(page);
      await page.goto("/demo/trips/plan");
      const presets = page.getByTestId("demo-presets");
      await expect(presets).toBeVisible({ timeout: 20_000 });
      await presets.getByRole("button").first().click();
      await expect(page.getByText(/Sample plan/).first()).toBeVisible({ timeout: 30_000 });
      await page.waitForTimeout(1500);
      expect(bad, bad.join("\n")).toEqual([]);
    });
  });
}
