import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";

// The public demo shell: every /demo/* route renders the real page, and a
// signed-out visitor never trips an endpoint outside the demo allowlist
// (scratchpad/demo-parity-ux-calls.md): no 401s, no console errors.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ALLOW: RegExp[] = [
  /^\/api\/demo$/, /^\/api\/config$/, /^\/api\/health$/,
  /^\/api\/vehicles$/, new RegExp(`^/api/vehicles/${VEHICLE_ID}/(now|pack-health|drain|trends|cold-range)$`),
  new RegExp(`^/api/state/${VEHICLE_ID}$`),
  /^\/api\/ledger\/(enabled|monthly|summary|trips|savings)$/,
  /^\/api\/charges(\/clusters)?$/, /^\/api\/drives$/, /^\/api\/samples$/, /^\/api\/sleep-activity$/,
  /^\/api\/settings\/(charging|charging\/networks|home-location)\/?$/,
  /^\/api\/trips\/saved$/, /^\/api\/trips\/saved\/[^/]+\/actual$/,
  /^\/api\/demo\/trips$/, /^\/api\/demo\/trips\/[a-z0-9-]+\/plan$/,
  /^\/api\/maps\/tiles\/(us|chargers)\.pmtiles$/,
];

function watch(page: Page) {
  const bad: string[] = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/") && !ALLOW.some((a) => a.test(u.pathname))) bad.push(`not allowlisted: ${r.method()} ${u.pathname}`);
  });
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/") && r.status() === 401) bad.push(`401: ${u.pathname}`);
  });
  page.on("console", (m) => {
    if (m.type() === "error" && !/tiles|pmtiles|Bad response code|Failed to load resource.*(404|416)/i.test(m.text())) bad.push(`console: ${m.text().slice(0, 140)}`);
  });
  // WebKit reports a fetch cut short by the next page.goto as "… due to access control checks". Every
  // request here is same-origin and stubbed, so that message can only be such an abort, not a CORS fault.
  page.on("pageerror", (e) => {
    if (!/due to access control checks/.test(e.message)) bad.push(`pageerror: ${e.message.slice(0, 140)}`);
  });
  return bad;
}

const ROUTES = [
  { name: "now", url: "/demo", text: /Sample truck\./ },
  { name: "activity", url: "/demo/activity", text: /Drives and charges/, heading: "Activity" },
  { name: "money", url: "/demo/money", text: /What charging actually cost/, heading: "Money" },
  { name: "plan", url: "/demo/trips/plan", text: /./ },
  { name: "truck", url: "/demo/truck", text: /Battery capacity/ },
  { name: "settings", url: "/demo/settings", text: /./ },
];

for (const r of ROUTES) {
  test(`demo route ${r.name}: renders, no 401s or console errors`, async ({ page }) => {
    await stubApi(page, { demo: true, signedIn: false });
    const bad = watch(page);
    await page.goto(r.url);
    await expect(page.getByText(r.text).first()).toBeVisible();
    // The shell nav is the app's, under /demo.
    await expect(page.getByRole("link", { name: "Activity" }).first()).toHaveAttribute("href", "/demo/activity");
    await page.waitForTimeout(1200);
    expect(bad, bad.join("\n")).toEqual([]);
  });
}

for (const [vpName, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`demo screenshots · ${vpName} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await stubApi(page, { demo: true, signedIn: false });
      const bad = watch(page);
      for (const r of ROUTES) {
        await page.goto(r.url);
        // On a phone the pages drop their subtitle line; the heading says where we are.
        const heading = "heading" in r ? r.heading : undefined;
        await expect(vpName === "iphone" && heading ? page.getByRole("heading", { name: heading }).first() : page.getByText(r.text).first()).toBeVisible();
        await page.waitForTimeout(900);
        await page.screenshot({ path: path.join(SHOTS, `demo-${r.name}-${vpName}-${theme}.png`), fullPage: vpName === "desktop" ? false : false });
      }
      expect(bad, bad.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("demo planner: preset trips render the real planner result; custom trips need a Rivian; nothing is POSTed", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  const bad = watch(page);
  const valhalla: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname.includes("/api/maps/valhalla")) valhalla.push(r.url());
  });
  const writes: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname.startsWith("/api/") && r.method() !== "GET") writes.push(`${r.method()} ${new URL(r.url()).pathname}`);
  });
  await page.goto("/demo/trips/plan");
  const presets = page.getByTestId("demo-presets");
  await expect(presets).toBeVisible();
  await expect(page.getByText("Sample trips planned for the demo truck.")).toBeVisible();
  await presets.getByRole("button", { name: /LA to Las Vegas/ }).click();
  // Plain copy: a sample plan, not a "saved snapshot from 1 min ago"; Plan vs actual says "Likely match", not "preview · high match".
  await expect(page.getByText(/Sample plan/).first()).toBeVisible();
  await expect(page.getByText(/Showing saved snapshot|min ago/)).toHaveCount(0);
  await expect(page.getByText(/preview ·|high match/)).toHaveCount(0);
  await expect(page.getByText(/Likely match/).first()).toBeVisible();
  // The real result: stops table with a charger, cost, arrival SoC.
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  await expect(page.getByText(/24%/).first()).toBeVisible();
  // No custom origin/destination form, no AI, no save/delete.
  await expect(page.getByRole("button", { name: "Plan trip" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Re-plan/ })).toHaveCount(0);
  await expect(page.getByText(/Analyze|AI advice/i)).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Save$/ })).toHaveCount(0);
  await expect(page.getByTestId("connect-hint").first()).toContainText("plan any trip");
  await page.waitForTimeout(800);
  // The default stub preset has NO route_polyline: the map must not fall
  // back to the routing proxy (401 for anonymous visitors).
  expect(valhalla).toEqual([]);
  expect(writes).toEqual([]);
  expect(bad, bad.join("\n")).toEqual([]);
});

for (const [vpName, vp, scheme] of [
  ["iphone", { width: 390, height: 844 }, "dark"],
  ["desktop", { width: 1280, height: 900 }, "light"],
  ["iphone", { width: 390, height: 844 }, "light"],
  ["desktop", { width: 1280, height: 900 }, "dark"],
] as const) {
  test(`demo planner preset screenshot · ${vpName} · ${scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: vp, colorScheme: scheme });
    const page = await ctx.newPage();
    await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), scheme);
    await stubApi(page, { demo: true, signedIn: false });
    await page.goto("/demo/trips/plan");
    await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
    await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
    await page.waitForTimeout(800);
    await page.addStyleTag({ content: "nav, header, [class*=fixed], [class*=sticky] { position: static !important; }" });
    await page.screenshot({ path: path.join(SHOTS, `demo-plan-preset-${vpName}-${scheme}.png`), fullPage: true });
    await ctx.close();
  });
}

test("demo planner: a missing Plan vs Actual (seed not run) shows no error and no page error", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  await page.route("**/api/trips/saved/*/actual", (r) => r.fulfill({ status: 404, contentType: "application/json", body: '{"error":"not found"}' }));
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/demo/trips/plan");
  await expect(page.getByTestId("demo-presets")).toBeVisible();
  await page.waitForTimeout(1200);
  // No Plan vs actual card content, no error copy.
  await expect(page.getByText(/Couldn't update|Planner failed|Something went wrong/)).toHaveCount(0);
  await expect(page.getByText("Was this your trip")).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("demo planner: a preset with a stored polyline still renders (straight legs are the fallback)", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  // Encoded precision-6 geometry LA -> Barstow -> Las Vegas (a few points).
  const enc = (v: number) => {
    let n = v < 0 ? ~(v << 1) : v << 1;
    let s = "";
    while (n >= 0x20) {
      s += String.fromCharCode((0x20 | (n & 0x1f)) + 63);
      n >>= 5;
    }
    return s + String.fromCharCode(n + 63);
  };
  const pts: [number, number][] = [[34.05, -118.24], [34.9, -117.02], [36.17, -115.14]];
  let pl = 0, po = 0, poly = "";
  for (const [la, lo] of pts) {
    const a = Math.round(la * 1e6), b = Math.round(lo * 1e6);
    poly += enc(a - pl) + enc(b - po);
    pl = a; po = b;
  }
  await page.route("**/api/demo/trips", (r) =>
    r.fulfill({
      json: {
        trips: [
          {
            id: "la-vegas", name: "LA to Las Vegas", saved_trip_id: "00000000-0000-4000-8000-000000000001", route_polyline: poly,
            inputs: { origin: { lat: 34.05, lon: -118.24, label: "Los Angeles" }, destination: { lat: 36.17, lon: -115.14, label: "Las Vegas" }, extra_stops: [] },
            summary: { distance_mi: 270, duration_min: 245, charge_stops: 1, arrival_soc_pct: 24 },
          },
        ],
      },
    }),
  );
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/demo/trips/plan");
  await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  await page.waitForTimeout(800);
  expect(errors).toEqual([]);
});

// The sample-trip pages link to /demo/trips/plan?preset=<id>, which opens that trip without a click.
test("demo planner: ?preset= opens that sample trip", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo/trips/plan?preset=la-vegas");
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
});

// The demo's Truck tab shows the cold-range card with sample figures and calls only the allowlisted endpoint.
test("demo Truck: the range-at-temperatures card shows sample figures", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  const bad = watch(page);
  await page.goto("/demo/truck");
  await expect(page.locator("[data-cold-range]")).toContainText("from your own drives (estimate)");
  await page.waitForTimeout(800);
  expect(bad, bad.join("\n")).toEqual([]);
});

test("demo money: the savings card is labelled as a sample truck with an example schedule, for the last full month only", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  const bad = watch(page);
  await page.goto("/demo/money");
  const card = page.locator("[data-savings]");
  await expect(card).toBeVisible();
  await expect(card.locator("[data-savings-sample]")).toHaveText("Sample truck · example schedule");
  await expect(card).toContainText("could have saved about $9.60 (estimate)");
  await expect(card).toContainText("Estimated from the sample truck's home charges and an example off-peak schedule, not anyone's real data.");
  await expect(card).not.toContainText("your own home charges");
  // The demo answers for one month, so there is no month switch.
  await expect(page.getByRole("button", { name: "This month so far" })).toHaveCount(0);
  await page.waitForTimeout(800);
  expect(bad, bad.join("\n")).toEqual([]);
});
