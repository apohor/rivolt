import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { DEMO_PLAN, stubApi } from "./stub";

// AI is off until the user turns it on: the Settings switch, the planner card and the drive-analysis card.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };
const j = (b: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(b) });

async function open(page: Page, o: { enabled: boolean; adviceStatus?: number }) {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  const puts: Record<string, unknown>[] = [];
  const advice: unknown[] = [];
  let enabled = o.enabled;
  await page.route("**/api/config", (r) =>
    r.fulfill(j({ features: { trip_planner_enabled: true, signup_enabled: true }, ai: { enabled: true }, tiles: { url: "/api/maps/tiles/us.pmtiles", chargers_url: "/api/maps/tiles/chargers.pmtiles" }, valhalla: { path: "/api/maps/valhalla" }, impersonation: { disabled: true } })),
  );
  await page.route("**/api/settings/ai-consent", (r) => {
    if (r.request().method() === "PUT") {
      const b = r.request().postDataJSON() as { enabled: boolean };
      puts.push(b);
      enabled = b.enabled;
    }
    return r.fulfill(j({ enabled }));
  });
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
  await page.route("**/api/trips/plan/advice", (r) => {
    advice.push(r.request().postDataJSON());
    return o.adviceStatus === 403
      ? r.fulfill(j({ error: "AI is off", class: "ai_off" }, 403))
      : r.fulfill(j({ headline: "Two stops", cost: [], efficiency: [], weather: [], vehicle: [], cost_estimate: { currency: "USD", dcfc_spend: 0, dcfc_spend_user_member: 0, dcfc_spend_all_member: 0 }, model: "t" }));
  });
  return { puts, advice };
}
async function planTrip(page: Page) {
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`Settings: the AI switch is off by default, says what is sent, and saves · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const { puts } = await open(page, { enabled: false });
    await page.goto("/settings?tab=account");
    const toggle = page.getByRole("switch", { name: "AI trip advice and drive analysis" });
    await expect(toggle).toHaveAttribute("aria-checked", "false");
    await expect(page.getByText(/Nothing is sent until you tap, and the text you type is never sent\./)).toBeVisible();
    await toggle.click();
    await expect.poll(() => puts.at(-1)).toEqual({ enabled: true });
    await expect(toggle).toHaveAttribute("aria-checked", "true");
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}

test("planner: with AI off the card says where to turn it on and there is no button", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const { advice } = await open(page, { enabled: false });
  await planTrip(page);
  const off = page.locator("[data-ai-off]");
  await expect(off).toContainText("Turn on AI advice in Settings");
  await expect(off.getByRole("link", { name: "Turn on AI advice in Settings" })).toHaveAttribute("href", "/settings?tab=account#ai-features");
  await expect(page.getByRole("button", { name: "Get AI advice" })).toHaveCount(0);
  expect(advice).toHaveLength(0);
  await ctx.close();
});

test("planner: with AI on there is a button; a 403 ai_off from the server shows the same pointer", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const { advice } = await open(page, { enabled: true, adviceStatus: 403 });
  await planTrip(page);
  await page.getByRole("button", { name: "Get AI advice" }).click();
  await expect.poll(() => advice.length).toBe(1);
  await expect(page.locator("[data-ai-off]")).toContainText("Turn on AI advice in Settings");
  await ctx.close();
});

test("drive analysis: with AI off the card points to Settings", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, { enabled: false });
  await page.route("**/api/drives/*/efficiency", (r) => r.fulfill(j({ error: "none" }, 404)));
  await page.goto("/drives");
  await page.getByRole("link").filter({ hasText: /mi/ }).first().click();
  await expect(page.locator("[data-ai-off]")).toContainText("Turn on AI analysis in Settings");
  await expect(page.getByRole("button", { name: "Analyze efficiency" })).toHaveCount(0);
  await ctx.close();
});
