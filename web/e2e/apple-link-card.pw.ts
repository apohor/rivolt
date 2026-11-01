import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// The Settings → Security → Apple card in every state it has (ux's review shots): one element shot per state at 390 and 1280 px, light and
// dark, against a stubbed API. The words are growth's (web/src/content/appleLinkCopy.ts); the states are what GET /api/auth/apple-link says
// (linked, has_password) and what Apple sent the person back with (?apple=<code>#security).
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

type Status = { available: boolean; linked: boolean; has_password: boolean };

async function open(page: Page, status: Status, url = "/settings#security") {
  await page.route("**/api/auth/apple-link", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(status) }));
  await page.goto(url);
  const card = page.locator("#apple");
  await expect(card).toBeVisible();
  return card;
}

const shot = (card: ReturnType<Page["locator"]>, name: string, vp: string, theme: string) =>
  card.screenshot({ path: path.join(SHOTS, `apple-link-${name}-${vp}-${theme}.png`) });

for (const [vp, size] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`${vp === "iphone" ? "iphone: " : ""}the Apple card in each state · ${vp} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: size, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await stubApi(page);

      // 1. Not connected, with a password; 2. the connect prompt open.
      let card = await open(page, { available: true, linked: false, has_password: true });
      await expect(card).toContainText("Lets you sign in with your Apple ID, including one that hides your email. Your password is asked for first.");
      await shot(card, "1-not-connected", vp, theme);
      await card.getByRole("button", { name: "Connect Apple" }).click();
      await expect(card.getByText("Connect Apple to this account")).toBeVisible();
      const pw = card.getByLabel("Your Rivolt password");
      await expect(pw).toHaveAttribute("type", "password");
      await expect(pw).toHaveAttribute("autocomplete", "current-password");
      await expect(card.getByRole("button", { name: "Continue to Apple" })).toBeDisabled(); // nothing typed yet
      await shot(card, "2-connect-prompt", vp, theme);
      // Cancel puts focus back on the button that opened it.
      await card.getByRole("button", { name: "Cancel" }).click();
      await expect(card.getByRole("button", { name: "Connect Apple" })).toBeFocused();

      // 3. Connected; 4. the disconnect confirm open.
      await page.unroute("**/api/auth/apple-link");
      card = await open(page, { available: true, linked: true, has_password: true });
      await expect(card.getByText("Apple: connected")).toBeVisible();
      await shot(card, "3-connected", vp, theme);
      await card.getByRole("button", { name: "Disconnect", exact: true }).click();
      await expect(card.getByText("Disconnect Apple?")).toBeVisible();
      await expect(card.getByRole("button", { name: "Disconnect Apple" })).toBeDisabled();
      await expect(card.getByRole("button", { name: "Keep it" })).toBeVisible();
      await shot(card, "4-disconnect-confirm", vp, theme);
      await card.getByRole("button", { name: "Keep it" }).click();
      await expect(card.getByRole("button", { name: "Disconnect", exact: true })).toBeFocused();

      // 5. Connected, no password: Disconnect is disabled and its reason is attached to it.
      await page.unroute("**/api/auth/apple-link");
      card = await open(page, { available: true, linked: true, has_password: false });
      const blocked = card.getByRole("button", { name: "Disconnect", exact: true });
      await expect(blocked).toBeDisabled();
      await expect(blocked).toHaveAccessibleDescription("Apple is the only way into this account, so it can't be disconnected.");
      await shot(card, "5-connected-no-password", vp, theme);

      // 6. Not connected, no password: the refusal and no connect button.
      await page.unroute("**/api/auth/apple-link");
      card = await open(page, { available: true, linked: false, has_password: false });
      await expect(card.getByText("Apple can't be connected to this account yet")).toBeVisible();
      await expect(card.getByText("Lets you sign in with your Apple ID")).toHaveCount(0); // no pitch above the refusal
      await expect(card.getByRole("button", { name: "Connect Apple" })).toHaveCount(0);
      await shot(card, "6-not-connected-no-password", vp, theme);

      // 7. What Apple sent the person back with: a success line (green) and the longest error line (rose); the query goes after it is shown.
      await page.unroute("**/api/auth/apple-link");
      card = await open(page, { available: true, linked: true, has_password: true }, "/settings?apple=linked#security");
      const ok = card.getByText("Apple connected. You can now sign in with your Apple ID.");
      await expect(ok).toBeVisible();
      await expect(ok).toHaveAttribute("data-tone", "good");
      await expect(page).not.toHaveURL(/apple=/);
      await shot(card, "7a-result-linked", vp, theme);
      await page.unroute("**/api/auth/apple-link");
      card = await open(page, { available: true, linked: false, has_password: true }, "/settings?apple=apple_in_use#security");
      const bad = card.getByText(/This Apple ID is already connected to a different Rivolt account\./);
      await expect(bad).toBeVisible();
      await expect(bad).toHaveAttribute("data-tone", "bad");
      await expect(page).not.toHaveURL(/apple=/);
      await shot(card, "7b-result-apple-in-use", vp, theme);

      // The card does not run past the page at either width.
      const box = (await card.boundingBox())!;
      expect(box.x + box.width).toBeLessThanOrEqual(size.width + 1);
      await ctx.close();
    });
  }
}

test("the Apple card is not shown where linking is off", async ({ page }) => {
  await stubApi(page);
  await page.route("**/api/auth/apple-link", (r) => r.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: "apple_not_available" }) }));
  // The card asked, was told no, and is not there (waiting on the answer, not on a timer).
  const asked = page.waitForResponse((r) => r.url().endsWith("/api/auth/apple-link"));
  await page.goto("/settings#security");
  expect((await asked).status()).toBe(404);
  await expect(page.locator("#apple")).toHaveCount(0);
});
