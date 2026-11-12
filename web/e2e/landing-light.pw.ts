import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The signed-out main page is dark only. A light-mode phone that once chose the light theme gets html[data-theme=light] before
// React runs, and that theme remaps --color-*: the page's text must not follow it (#1956: dark headings on the dark page).
test("@webkit main page keeps its colours under the light theme", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page, { signedIn: false, landing: true });
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  const colours = () =>
    page.evaluate(() =>
      Array.from(document.querySelectorAll<HTMLElement>(".lp *"))
        .filter((el) => Array.from(el.childNodes).some((n) => n.nodeType === Node.TEXT_NODE && n.textContent?.trim()))
        .map((el) => `${el.tagName.toLowerCase()}.${String(el.className).slice(0, 30)} ${getComputedStyle(el).color}`),
    );
  const h1 = () => page.getByRole("heading", { level: 1 }).evaluate((el) => getComputedStyle(el).color);
  const [darkH1, dark] = [await h1(), await colours()];
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  expect(await h1()).toBe(darkH1);
  expect(await colours()).toEqual(dark);
});
