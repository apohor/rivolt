import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// WCAG 1.4.10 reflow on Settings: at 320 px wide with text at 200% (a 400% zoom of a 1280 px window) nothing runs off the side. Every
// Settings section is opened through its own tab, and every element wider than the window is named when one is.
test("Settings at 320 px and 200% text: no section runs off the side", async ({ browser }) => {
  test.setTimeout(120_000);
  const ctx = await browser.newContext({ viewport: { width: 320, height: 640 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.goto("/settings");
  await page.addStyleTag({ content: "html { font-size: 200% !important; }" });
  const nav = page.getByRole("navigation", { name: /sections/i });
  await expect(nav).toBeVisible();
  const hrefs = await nav.getByRole("link").evaluateAll((els) => els.map((e) => (e as HTMLAnchorElement).getAttribute("href") || ""));
  expect(hrefs.length).toBeGreaterThan(1);
  for (const href of hrefs) {
    await page.goto(href);
    await page.addStyleTag({ content: "html { font-size: 200% !important; }" });
    await expect(nav).toBeVisible();
    const wide = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>("body *")]
        .filter((el) => el.getBoundingClientRect().right > window.innerWidth + 1 && getComputedStyle(el).position !== "fixed" && !el.closest("[data-scroll-x]"))
        .slice(0, 8)
        .map((el) => `${el.tagName.toLowerCase()}${el.id ? "#" + el.id : ""}.${String(el.className).split(" ").slice(0, 3).join(".")} right=${Math.round(el.getBoundingClientRect().right)} "${(el.textContent || "").trim().slice(0, 30)}"`),
    );
    expect(wide, `${href}: elements wider than the window`).toEqual([]);
  }
  await ctx.close();
});
