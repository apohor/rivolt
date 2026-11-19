import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";
import { currentMonth, ledgerMonthFixture } from "../src/lib/ledger";
import { e2eNow } from "./clock";

// With two or more trucks Money shows a vehicle picker beside the month picker. At a phone width the pickers must wrap
// under the title instead of drawing over it; the title, the vehicle picker and the month picker may not overlap.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const NOW = new Date(e2eNow());

for (const [name, vp] of [
  ["390", { width: 390, height: 844 }],
  ["320", { width: 320, height: 640 }],
  ["1280", { width: 1280, height: 900 }],
] as const) {
  for (const scheme of ["light", "dark"] as const) {
    test(`money header: vehicle picker clears the title · ${name} ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await stubApi(page, { scenario: "parked" });
      await page.route("**/api/vehicles", (r) =>
        r.fulfill({
          json: [
            { id: "veh-sample-1", vin: "SAMPLE0000000001", name: "Sample R1S Quad Max", model: "R1S", pack_kwh: 135 },
            { id: "veh-sample-2", vin: "SAMPLE0000000002", name: "Second truck with a long name", model: "R1T", pack_kwh: 135 },
          ],
        }),
      );
      await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ json: ledgerMonthFixture(currentMonth(NOW), NOW) }));
      await page.goto("/money");
      const picker = page.getByLabel("Vehicle");
      await expect(picker).toBeVisible();
      const box = async (l: ReturnType<typeof page.locator>) => (await l.boundingBox())!;
      // The h1 box itself can be squeezed to nothing, so measure where its text is drawn.
      const title = await page.getByRole("heading", { name: "Money", level: 1 }).evaluate((h) => {
        const r = document.createRange();
        r.selectNodeContents(h);
        const b = r.getBoundingClientRect();
        return { x: b.x, y: b.y, width: b.width, height: b.height };
      });
      const veh = await box(picker.locator("xpath=ancestor::label"));
      const month = await box(page.locator("[data-ui-header] button, [data-ui-header] select, [data-ui-header] input").last());
      const apart = (a: typeof title, b: typeof title) => a.x + a.width <= b.x + 0.5 || b.x + b.width <= a.x + 0.5 || a.y + a.height <= b.y + 0.5 || b.y + b.height <= a.y + 0.5;
      expect(apart(title, veh), "title and vehicle picker do not overlap").toBe(true);
      expect(apart(veh, month), "vehicle and month pickers do not overlap").toBe(true);
      expect(veh.x + veh.width, "vehicle picker inside the viewport").toBeLessThanOrEqual(vp.width);
      expect(month.x + month.width, "month picker inside the viewport").toBeLessThanOrEqual(vp.width);
      await page.screenshot({ path: path.join(SHOTS, `money-header-${name}-${scheme}.png`) });
      await ctx.close();
    });
  }
}
