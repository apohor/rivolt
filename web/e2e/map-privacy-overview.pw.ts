import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The activity maps (drives and charges overview) and the charge page's map, like the drive map (map-privacy.pw.ts): MapLibre on the app's
// own tiles, no request leaves the origin, no CSP rule trips under the server's real policy, and without WebGL the page says so and its lists
// still show. "iphone:" in the titles runs these in WebKit too.

const CSP =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https://rivian.com https://*.rivian.com; " +
  "font-src 'self' data:; connect-src 'self'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'";

const open = async (page: import("@playwright/test").Page) => {
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
  });
  await stubApi(page);
};

test("iphone: the activity maps make no third-party request and trip no CSP rule", async ({ page }) => {
  await open(page);
  await page.route(
    (u) => u.pathname === "/activity",
    async (route) => {
      if (route.request().resourceType() !== "document") return route.fallback();
      const res = await route.fetch();
      return route.fulfill({ response: res, headers: { ...res.headers(), "content-security-policy": CSP } });
    },
  );
  await page.addInitScript(() => {
    (window as unknown as { __csp: string[] }).__csp = [];
    document.addEventListener("securitypolicyviolation", (e) => (window as unknown as { __csp: string[] }).__csp.push(`${e.violatedDirective} ${e.blockedURI}`));
  });
  const foreign: string[] = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.protocol === "data:" || u.protocol === "blob:") return;
    if (u.hostname !== "127.0.0.1" && u.hostname !== "localhost") foreign.push(r.url());
  });
  await page.goto("/activity?view=map");
  await expect(page.locator("[data-activity-view='map'] .maplibregl-canvas").first()).toBeVisible();
  // The charges layer too.
  await page.getByRole("group", { name: "Map layer" }).getByRole("button", { name: /Charges/ }).click();
  await expect(page.locator("[data-activity-view='map'] section[aria-label='Charges'] .maplibregl-canvas")).toBeVisible();
  await page.waitForTimeout(1000);
  expect(foreign).toEqual([]);
  expect(await page.evaluate(() => (window as unknown as { __csp: string[] }).__csp)).toEqual([]);
});

test("iphone: without WebGL the activity map says so and the table still shows", async ({ page }) => {
  await page.addInitScript(() => {
    const orig = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, type: string, ...rest: unknown[]) {
      if (type === "webgl" || type === "webgl2" || type === "experimental-webgl") return null;
      return (orig as (...a: unknown[]) => unknown).call(this, type, ...rest);
    } as typeof orig;
  });
  await open(page);
  await page.goto("/activity?view=map");
  await expect(page.locator("[data-activity-view='map'] [data-map-unavailable]").first()).toBeVisible();
  await expect(page.locator(".maplibregl-canvas")).toHaveCount(0);
  await page.getByRole("group", { name: "View" }).getByRole("button", { name: "Table" }).click();
  await expect(page.locator("[data-activity-view='table'] table").first()).toBeVisible();
});
