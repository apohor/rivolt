import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";

// The public demo (signed out, read-only) with design key now_parked_graph on: the Now card draws "Battery while parked" from
// GET /api/vehicles/{id}/parked-series, a route the demo serves for the sample truck, and no /api request on the page fails (the
// card used to hide the map while a 404 on that route was pending).
const KEYS = ["now_v2", "now_tidy", "now_slot", "now_parked_graph"];

test("demo Now with the parked graph key on: the graph renders and no request fails @webkit", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false, nowEndpoint: true, scenario: "parked", designPreview: KEYS, parkedCase: "overnight" });
  const bad: string[] = [];
  let asked = 0;
  page.on("request", (r) => {
    if (new URL(r.url()).pathname === `/api/vehicles/${VEHICLE_ID}/parked-series`) asked++;
  });
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/") && r.status() >= 400 && !/tiles|pmtiles/.test(u.pathname)) bad.push(`${r.status()} ${u.pathname}`);
  });
  await page.goto("/demo");
  const g = page.getByTestId("now-parked-graph");
  await expect(g).toBeVisible();
  await expect(g).toContainText("Battery while parked");
  await expect(g.getByTestId("now-parked-sleep").first()).toBeVisible();
  expect(asked).toBeGreaterThan(0);
  expect(bad, bad.join("\n")).toEqual([]);
});
