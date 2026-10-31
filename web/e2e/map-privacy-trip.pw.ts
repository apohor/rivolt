import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The planner's route map, like the drive map (map-privacy.pw.ts): MapLibre on the app's own tiles, no request leaves the origin, no CSP
// rule trips under the server's real policy, the credits open and close on a tap, and without WebGL the itinerary stands with a plain
// "map unavailable" note. "iphone:" in the titles runs these in WebKit too. Synthetic preset trip, nothing real.

const CSP =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https://rivian.com https://*.rivian.com; " +
  "font-src 'self' data:; connect-src 'self'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'";

async function openPlan(page: import("@playwright/test").Page) {
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo/trips/plan");
  await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
}

test("iphone: the trip map makes no third-party request and trips no CSP rule", async ({ page }) => {
  await page.route(
    (u) => u.pathname.startsWith("/demo/"),
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
  await openPlan(page);
  await expect(page.locator("[data-plan-area='map'] .maplibregl-canvas").first()).toBeVisible();
  await expect(page.locator("[data-plan-area='map'] [data-map-idle]").first()).toBeAttached();
  expect(foreign).toEqual([]);
  expect(await page.evaluate(() => (window as unknown as { __csp: string[] }).__csp)).toEqual([]);
});

test("iphone: the trip map's (i) opens and closes the credits", async ({ page }) => {
  await openPlan(page);
  const attr = page.locator("[data-plan-area='map'] .maplibregl-ctrl-attrib").first();
  const button = attr.locator(".maplibregl-ctrl-attrib-button");
  const links = [attr.getByText("Protomaps"), attr.getByText("OpenStreetMap")];
  await expect(button).toBeVisible();
  for (const l of links) await expect(l).toBeHidden();
  await button.click();
  for (const l of links) await expect(l).toBeVisible();
  await button.click();
  for (const l of links) await expect(l).toBeHidden();
});

test("iphone: without WebGL the planner says the map is unavailable and the itinerary still shows", async ({ page }) => {
  await page.addInitScript(() => {
    const orig = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, type: string, ...rest: unknown[]) {
      if (type === "webgl" || type === "webgl2" || type === "experimental-webgl") return null;
      return (orig as (...a: unknown[]) => unknown).call(this, type, ...rest);
    } as typeof orig;
  });
  await openPlan(page);
  await expect(page.locator("[data-map-unavailable]").first()).toBeVisible();
  await expect(page.locator(".maplibregl-canvas")).toHaveCount(0);
  await expect(page.getByText(/Barstow/).first()).toBeVisible();
});
