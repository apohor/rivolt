import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Design key pro_entry (D4 A): a small "Pro tier" link in the header of the signed-in app, free and lapsed accounts only, one tap to the Pro
// sheet. Key off: no link. Run at 393 and 1440, light and dark, and the stills are written for a person to open.
const fixture = (n: string) => JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", n), "utf8")) as Record<string, unknown>;
const free = fixture("me_plan_free.json");
const founding = fixture("me_plan_founding.json");
const SHOTS = process.env.PRO_ENTRY_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function open(page: Page, keys: string[], me: Record<string, unknown>) {
  await stubApi(page, { designPreview: keys });
  await page.route("**/api/auth/me", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(me) }));
  await page.goto("/");
  await expect(page.getByRole("link", { name: "Settings" }).first()).toBeVisible();
}
const link = (page: Page) => page.getByRole("banner").getByRole("button", { name: "Pro tier" });

for (const theme of ["light", "dark"] as const) {
  for (const w of [393, 1440] as const) {
    test(`pro_entry header link @webkit · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w < 500 ? 852 : 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);

      // Key on, a free account: the link is there, in the header, beside Settings, and nothing overflows.
      await open(page, ["pro_entry"], free);
      await expect(link(page)).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const l = (await link(page).boundingBox())!;
      const s = (await page.getByRole("link", { name: "Settings" }).first().boundingBox())!;
      expect(l.x + l.width).toBeLessThanOrEqual(s.x + 1); // not over the person button
      await page.screenshot({ path: path.join(SHOTS, `pro-entry-on-${w}-${theme}.png`), clip: { x: 0, y: 0, width: w, height: 160 } });

      // One tap opens the sheet; Escape closes it and the link is still there.
      await link(page).click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await page.screenshot({ path: path.join(SHOTS, `pro-entry-sheet-${w}-${theme}.png`) });
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect(link(page)).toBeVisible();

      // A founding (Pro) account never sees it, key on.
      await open(page, ["pro_entry"], founding);
      await expect(link(page)).toHaveCount(0);

      // Key off: no link for a free account, as before.
      await open(page, [], free);
      await expect(link(page)).toHaveCount(0);
      await page.screenshot({ path: path.join(SHOTS, `pro-entry-off-${w}-${theme}.png`), clip: { x: 0, y: 0, width: w, height: 160 } });
      await ctx.close();
    });
  }
}
