import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// A drive or charge detail page asks for about 2,000 samples, not 20,000 / 10,000: the server buckets a window over the
// limit across its whole span, so the charts keep their shape and a tenth of the data moves.

test("drive and charge detail ask the samples endpoint for at most 2,000 rows", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  const limits: string[] = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname === "/api/samples") limits.push(u.searchParams.get("limit") ?? "");
  });
  await page.goto("/drives");
  await page.getByRole("link").filter({ hasText: /mi/ }).first().click();
  await expect.poll(() => limits.length).toBeGreaterThan(0);
  const afterDrive = limits.length;
  await page.goto("/charges");
  await page.getByRole("link").filter({ hasText: /kWh/ }).first().click();
  await expect.poll(() => limits.length).toBeGreaterThan(afterDrive);
  for (const l of limits) expect(Number(l)).toBeLessThanOrEqual(2000);
  expect(limits).toContain("2000");
  await ctx.close();
});
