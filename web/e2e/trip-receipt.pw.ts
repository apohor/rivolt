import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Share this trip as a receipt (design key trip_receipt): the Saved tab's Share opens an image made on the device, never the link sheet. The
// route sketch and the cost are off on every open.
const j = (b: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(b) });
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };
const RECEIPT = {
  depart_at: "2026-11-21T15:00:00Z", tz: "America/Los_Angeles", start_soc: 90, start: "Pasadena area", end: "Las Vegas area",
  rows: [
    { kind: "drive", from: "Pasadena area", to: "Example Fast Charge", dist_m: 160934, dur_s: 6000, from_soc: 90, to_soc: 41, rounded: true },
    { kind: "charge", name: "Example Fast Charge · Barstow", dur_s: 1080, from_soc: 41, to_soc: 80, max_kw: 250 },
    { kind: "drive", from: "Example Fast Charge", to: "Las Vegas area", dist_m: 241401, dur_s: 8400, from_soc: 80, to_soc: 30, rounded: true },
  ],
  totals: { dist_m: 402335, drive_s: 14400, charge_s: 1080, stops: 1, arrive_at: "2026-11-21T21:00:00Z", arrival_soc: 30 },
  sketch: ["_p~iF~ps|U_ulLnnqC"],
};

async function open(page: Page, key: boolean) {
  await stubApi(page, { tripShare: true, designPreview: key ? ["trip_receipt"] : [] });
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Home to Denver", inputs: INPUTS, plan: DEMO_PLAN, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  const bodies: { sketch: boolean; metric: boolean }[] = [];
  await page.route("**/api/trips/saved/st-a/receipt", (r) => {
    bodies.push(r.request().postDataJSON());
    return r.fulfill(j(RECEIPT));
  });
  let links = 0;
  await page.route("**/api/trips/saved/st-a/share*", (r) => { links++; return r.fulfill(j({ links: [] })); });
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  return { bodies, links: () => links };
}

test("Share on a saved trip opens the receipt, not the link sheet, with sketch and cost off every time @webkit", async ({ page }) => {
  const { bodies, links } = await open(page, true);
  const share = page.getByTestId("trip-share-open");
  const box = await share.boundingBox();
  expect(box!.height).toBeGreaterThanOrEqual(44);
  expect(box!.width).toBeGreaterThanOrEqual(44);
  await share.click();
  const sheet = page.getByTestId("trip-receipt-sheet");
  await expect(sheet).toBeVisible();
  await expect(page.getByTestId("trip-share-sheet")).toHaveCount(0);
  await expect(sheet.getByTestId("trip-receipt-says")).toContainText(/start and destination areas, your departure day and time, and the charging stops/);
  await expect(sheet.getByTestId("trip-receipt-says")).toContainText(/Stops you added show only as an area/);
  await expect(sheet.getByTestId("trip-receipt-sketch")).not.toBeChecked();
  await expect(sheet.getByTestId("trip-receipt-cost")).not.toBeChecked();
  await expect.poll(() => bodies.length).toBe(1);
  expect(bodies[0].sketch).toBe(false);
  await sheet.getByTestId("trip-receipt-sketch").check();
  await expect.poll(() => bodies.length).toBe(2);
  expect(bodies[1].sketch).toBe(true);
  await sheet.getByTestId("trip-receipt-cost").check();
  await expect(sheet.getByRole("button", { name: "Save image" })).toBeEnabled();
  const dl = page.waitForEvent("download");
  await sheet.getByRole("button", { name: "Save image" }).click();
  const file = await dl;
  expect(file.suggestedFilename()).toBe("rivolt-trip-plan.png");
  if (process.env.RECEIPT_EXPORT) await file.saveAs(process.env.RECEIPT_EXPORT);
  await page.screenshot({ path: "e2e/__screenshots__/trip-receipt-sheet.png" });
  await page.keyboard.press("Escape");
  await expect(sheet).toBeHidden();
  // Reopened: both toggles are off again.
  await share.click();
  await expect(sheet.getByTestId("trip-receipt-sketch")).not.toBeChecked();
  await expect(sheet.getByTestId("trip-receipt-cost")).not.toBeChecked();
  expect(links()).toBe(0);
});

test("without the key Share still opens the link sheet", async ({ page }) => {
  await open(page, false);
  await page.getByTestId("trip-share-open").click();
  await expect(page.getByTestId("trip-share-sheet")).toBeVisible();
  await expect(page.getByTestId("trip-receipt-sheet")).toHaveCount(0);
});
