import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// The plan map's origin and destination pills sit on the side of the pin away from the route and clear of its line. This fixture is the hard
// case: a straight west-to-east route, so each end sits at the view's edge with the line leaving along the only side that has room for a pill.
// The pills must go above (or below) the pin there, not along the line. Made-up coordinates near (10, 10). The shots are for ux and qa: the
// pills are canvas images, so a pixel is the only thing to look at. The mid-route stop's pill must still show its details: the end pills
// sit above the line, so it takes the other side of the stop (below the line) instead of being dropped.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

// A precision-6 polyline of (lat, lon) points.
function encode6(pts: [number, number][]): string {
  let out = "";
  let pLa = 0;
  let pLo = 0;
  const put = (v: number) => {
    let u = v < 0 ? ~(v << 1) : v << 1;
    while (u >= 0x20) {
      out += String.fromCharCode((0x20 | (u & 0x1f)) + 63);
      u >>= 5;
    }
    out += String.fromCharCode(u + 63);
  };
  for (const [la, lo] of pts) {
    const a = Math.round(la * 1e6);
    const b = Math.round(lo * 1e6);
    put(a - pLa);
    put(b - pLo);
    pLa = a;
    pLo = b;
  }
  return out;
}

const route0 = DEMO_PLAN.Routes[0];
const line: [number, number][] = Array.from({ length: 41 }, (_, i) => [10, 10 + i * 0.02]);
const PLAN = {
  ...DEMO_PLAN,
  Routes: [
    {
      ...route0,
      route_polyline: encode6(line),
      Waypoints: [
        { ...route0.Waypoints[0], Name: "Westgate Depot", Latitude: 10, Longitude: 10 },
        { ...route0.Waypoints[1], Name: "Midway Charge", Latitude: 10, Longitude: 10.4 },
        { ...route0.Waypoints[2], Name: "Eastgate Terminal", Latitude: 10, Longitude: 10.8 },
      ],
    },
  ],
};

for (const theme of ["dark", "light"] as const) {
  test(`iphone: a west-to-east route's end pills clear the line · ${theme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
    await stubApi(page, { demo: true, signedIn: false });
    await page.route(/\/api\/demo\/trips\/la-vegas\/plan$/, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(PLAN) }));
    await page.goto("/demo/trips/plan");
    await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
    const map = page.locator("[data-plan-area='map']");
    await expect(map.locator("[data-map-drawn]:not([data-map-drawn='0'])").first()).toBeAttached();
    await expect(map.locator(".maplibregl-marker")).toHaveCount(3);
    // The three pins sit on one horizontal line (a straight west-to-east route): the end pins are at the edges, with the line on their only roomy side.
    const ys = await map.locator(".maplibregl-marker").evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top)));
    expect(Math.max(...ys) - Math.min(...ys)).toBeLessThanOrEqual(2);
    await map.screenshot({ path: path.join(SHOTS, `plan-map-pills-west-east-${theme}.png`) });
    await ctx.close();
  });
}
