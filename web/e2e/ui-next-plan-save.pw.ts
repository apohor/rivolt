import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { DEMO_PLAN, stubApi } from "./stub";

// Saving a trip from the result itself: a fresh plan (nothing loaded from Saved), single-day and multi-day.
// Owner report (prod 0.37.1): Save trip did nothing, and the server never saw a POST /api/trips/saved, because
// the only trip Save was a name field on the Saved tab and the star by each place saves a place.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const day = (i: number) => ({
  index: i,
  plan: DEMO_PLAN,
  depart_at: "2026-10-20T13:00:00Z",
  arrive_at: "2026-10-20T20:00:00Z",
  start: { latitude: 41, longitude: -87 },
  end: { latitude: 41, longitude: -93 },
  departure_soc: 90,
  arrival_soc: 24,
  destination_reached: true,
  soc_below_limit: false,
  ...(i === 1 ? { overnight: { name: "Des Moines", parked_hours: 11, l2_kw: 7, added_kwh: 52, post_charge_soc_pct: 80, capped: true } } : {}),
  costs: DEMO_PLAN.costs,
});

type Saved = { name: string; inputs: Record<string, unknown>; plan?: Record<string, unknown> };

async function fresh(page: Page) {
  await stubApi(page);
  const created: Saved[] = [];
  const list: { id: string; name: string }[] = [];
  await page.route("**/api/trips/saved", (r) => {
    if (r.request().method() === "POST") {
      const b = r.request().postDataJSON() as Saved;
      created.push(b);
      list.push({ id: `n${created.length}`, name: b.name });
      return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ id: `n${created.length}`, ...b, created_at: "2026-10-18T00:00:00Z", updated_at: "2026-10-18T00:00:00Z" }) });
    }
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(list.map((t) => ({ ...t, inputs: { origin: { lat: 1, lon: 2, label: "Home" }, destination: { lat: 3, lon: 4, label: "Omaha" }, extra_stops: [] }, created_at: "2026-10-18T00:00:00Z", updated_at: "2026-10-18T00:00:00Z" }))) });
  });
  await page.route("**/api/geocode*", (r) => {
    const q = new URL(r.request().url()).searchParams.get("q") ?? "";
    const lat = q.startsWith("Des") ? 41.59 : 41.26;
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ name: q, admin1: "US", country: "", latitude: lat, longitude: -95 }]) });
  });
  await page.route("**/api/trips/plan-multiday", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ days: [day(1), day(2)], total: { distance_meters: 900_000, drive_duration_sec: 30_000, charging_duration_sec: 3_000 } }) }));
  await page.route("**/api/trips/plan", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(DEMO_PLAN) }));
  await page.goto("/trips/plan");
  const bar = page.locator("[data-plan-bar]");
  await bar.locator("[data-plan-field][data-heading='From']").getByRole("button", { name: /Home/ }).click();
  const to = bar.locator("[data-plan-field][data-heading='To']");
  await to.getByPlaceholder(/To/).fill("Omaha");
  await page.getByRole("button", { name: /Omaha/ }).first().click();
  return { bar, created, list };
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`a fresh multi-day result saves from the result and shows under Saved · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const { bar, created } = await fresh(page);
    // Saving needs no trip loaded first, and the place star says it saves a place.
    await expect(bar.getByRole("button", { name: /^Save place:/ }).first()).toContainText("save place");
    await bar.getByRole("button", { name: "+ Add an overnight" }).click();
    await bar.getByPlaceholder("Search city or place…").fill("Des Moines");
    await page.getByRole("button", { name: /Des Moines/ }).first().click();
    await page.getByRole("button", { name: "Plan 2-day trip" }).click();
    await expect(page.locator("[data-day-card]")).toHaveCount(2);

    const save = page.locator("[data-save-trip]");
    await expect(save.getByRole("button", { name: "Save trip" })).toBeVisible();
    expect(created).toHaveLength(0);
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SHOTS, `save-bar-${v.n}-dark.png`) });
    await save.getByRole("button", { name: "Save trip" }).click();
    // A name is suggested, so it is one more tap.
    const name = save.getByLabel("Name this trip");
    await expect(name).toHaveValue(/^Home to Omaha.* · 2 days$/);
    await name.fill("Home to Omaha");
    await save.getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(() => created.length).toBe(1);
    expect(created[0].name).toBe("Home to Omaha");
    expect(created[0].plan).toMatchObject({ kind: "multiday" });
    expect((created[0].plan as { days: unknown[] }).days).toHaveLength(2);
    await expect(save.getByRole("status")).toContainText("Saved as “Home to Omaha”");
    await save.getByRole("button", { name: "See Saved trips" }).click();
    await expect(page.getByRole("button", { name: /Home to Omaha/ })).toBeVisible();
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container, .maplibregl-map").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}

test("a fresh single-day result saves from the result too", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const { created } = await fresh(page);
  await page.getByRole("button", { name: "Plan trip" }).click();
  const save = page.locator("[data-save-trip]");
  await expect(save).toBeVisible();
  await save.getByRole("button", { name: "Save trip" }).click();
  await expect(save.getByLabel("Name this trip")).toHaveValue(/^Home to Omaha/);
  await save.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => created.length).toBe(1);
  expect(created[0].plan).toHaveProperty("Routes");
  await ctx.close();
});

test("the Saved tab suggests a name, so Save is never a dead button", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const { created } = await fresh(page);
  await page.getByRole("tab", { name: "Saved" }).click();
  await expect(page.getByPlaceholder("Save current trip as…")).toHaveValue(/^Home to Omaha/);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => created.length).toBe(1);
  await ctx.close();
});

test("a failed save says why on the result", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await fresh(page);
  await page.route("**/api/trips/saved", (r) => (r.request().method() === "POST" ? r.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: "a trip with that name already exists" }) }) : r.fulfill({ status: 200, contentType: "application/json", body: "[]" })));
  await page.getByRole("button", { name: "Plan trip" }).click();
  const save = page.locator("[data-save-trip]");
  await save.getByRole("button", { name: "Save trip" }).click();
  await save.getByRole("button", { name: "Save", exact: true }).click();
  await expect(save.getByRole("alert")).toContainText("a trip with that name already exists");
  await ctx.close();
});
