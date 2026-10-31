import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID, vehicleState } from "./stub";
import { e2eNow } from "./clock";

// Truck software card: the installed and the pending update each link to their release
// notes through our endpoint, which asks Rivian for a fresh link when it is clicked (Rivian's own
// is a one-hour signed URL, so it is never stored or listed): new tab, noopener noreferrer;
// nothing renders without has_notes.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const VEHICLE = { id: VEHICLE_ID, vin: "SAMPLE0000000001", name: "Sample R1S", model: "R1S", pack_kwh: 135 };
const OTA = {
  current: { version: "2026.30.0", has_notes: true, locale: "en-US" },
  available: { version: "2026.38.1", has_notes: true, locale: "en-US" },
  status: "ready_to_install",
  fetched_at: new Date(e2eNow()).toISOString(),
};

async function open(page: import("@playwright/test").Page, ota: unknown) {
  await stubApi(page, { scenario: "parked", drain: "ok" });
  await page.route("**/api/vehicles", (r) => r.fulfill({ json: [{ ...VEHICLE, ...(ota ? { ota } : {}) }] }));
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) =>
    r.fulfill({ json: { ...vehicleState("parked"), ota_current_version: "2026.30.0", ota_available_version: "2026.38.1" } }),
  );
  await page.goto("/truck");
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`release notes links · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await open(page, OTA);
      const links = page.locator("[data-ota-notes]");
      await expect(links).toHaveCount(2);
      for (const a of await links.all()) {
        await expect(a).toHaveAttribute("target", "_blank");
        await expect(a).toHaveAttribute("rel", "noopener noreferrer");
        await expect(a).toHaveAttribute("href", /^\/api\/vehicles\/[^/]+\/ota\/notes\?which=(current|available)$/);
      }
      await expect(links.first()).toHaveAttribute("href", `/api/vehicles/${VEHICLE_ID}/ota/notes?which=current`);
      await expect(links.nth(1)).toHaveAttribute("href", `/api/vehicles/${VEHICLE_ID}/ota/notes?which=available`);
      await links.first().scrollIntoViewIfNeeded();
      await page.waitForTimeout(400);
      await page.screenshot({ path: path.join(SHOTS, `truck-ota-${w}-${theme}.png`) });
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".maplibregl-map").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("no links without ota details or without has_notes", async ({ page }) => {
  await open(page, undefined);
  await expect(page.getByText("2026.30.0")).toBeVisible();
  await expect(page.locator("[data-ota-notes]")).toHaveCount(0);
  await page.unrouteAll({ behavior: "ignoreErrors" });
  await open(page, { ...OTA, current: { ...OTA.current, has_notes: false }, available: { version: "2026.38.1", locale: "en-US" } });
  await expect(page.getByText("2026.38.1")).toBeVisible();
  await expect(page.locator("[data-ota-notes]")).toHaveCount(0);
});
