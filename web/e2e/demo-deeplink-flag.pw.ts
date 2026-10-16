import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// demo.rivolt.dev v0.36.1: only /demo got the new look; a deep link to /demo/activity,
// /demo/money or /demo/truck rendered the classic UI, because nothing on those pages asked
// for the server flag. The demo layout now does.
for (const tab of ["", "/activity", "/money", "/truck", "/trips/plan"]) {
  test(`demo deep link ${tab || "/"} gets the server-flagged new look`, async ({ page }) => {
    await stubApi(page, { demo: true, signedIn: false, uiNext: true });
    await page.goto(`/demo${tab}`);
    await expect(page.locator("html")).toHaveAttribute("data-ui", "next");
  });
}

test("flag off on the server: the demo deep link stays classic", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo/activity");
  await expect(page.getByRole("heading", { name: "Activity" }).first()).toBeVisible();
  await expect(page.locator("html")).not.toHaveAttribute("data-ui", "next");
});
