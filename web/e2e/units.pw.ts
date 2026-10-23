import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, vehicleState, VEHICLE_ID } from "./stub";

// Distance units: miles by default, kilometers on request, on the
// Money receipts and the Now card.

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const VIEWS = [
  { name: "iphone", viewport: { width: 390, height: 844 } },
  { name: "desktop", viewport: { width: 1280, height: 800 } },
] as const;

for (const v of VIEWS) {
  for (const scheme of ["dark", "light"] as const) {
    for (const unit of ["mi", "km"] as const) {
      test(`money + now · ${v.name} · ${scheme} · ${unit}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: v.viewport, colorScheme: scheme });
        const page = await ctx.newPage();
        await stubApi(page, { scenario: "parked" });
        // Parked ~42 km from the sample home.
        await page.route(`**/api/state/${VEHICLE_ID}`, (r) =>
          r.fulfill({ json: { ...vehicleState("parked"), latitude: 40.38, longitude: -100.0 } }),
        );
        await page.addInitScript(
          ([u, s]) => {
            localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: u }));
            localStorage.setItem("rivolt.theme", s);
          },
          [unit, scheme],
        );
        await page.goto("/money");
        await expect(page.getByText(new RegExp(`\\d+ ${unit} from home`)).first()).toBeVisible();
        await expect(page.getByText(new RegExp(`\\d+ ${unit === "mi" ? "km" : "mi"} from home`))).toHaveCount(0);
        const row = page.getByText(new RegExp(`\\d+ ${unit} from home`)).first().locator("xpath=ancestor::li[1]");
        await row.scrollIntoViewIfNeeded();
        await row.screenshot({ path: path.join(SHOTS, `units-money-${v.name}-${scheme}-${unit}.png`) });
        await page.goto("/");
        await expect(page.getByText(new RegExp(`Parked · [\\d.]+ ${unit} from home`))).toBeVisible();
        await page.getByRole("region", { name: "Now" }).locator(":scope > *").first().screenshot({ path: path.join(SHOTS, `units-now-${v.name}-${scheme}-${unit}.png`) });
        await ctx.close();
      });
    }
  }
}
