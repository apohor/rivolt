import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID, vehicleState, type NowScenario } from "./stub";

// Design "charge_pulse": while power flows the Live "Charging session" panel and the Now card's charging state show a soft green pulse (a bolt
// beside the kW figure, a glow at the progress bar's live end, the battery line's live dot); plugged in and waiting is a still plug badge. Off,
// both are as they were. Reduced motion keeps the glow and drops the animation. SHOTS_DIR makes the owner's stills.
const FULL = JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8"));
const SHOTS = process.env.SHOTS_DIR;

async function live(page: Page, keys: string[], scenario: NowScenario = "charging", patch: Record<string, unknown> = {}) {
  await stubApi(page, { scenario, liveSamples: true, designPreview: keys });
  const state = { ...FULL, ...vehicleState(scenario), ...patch };
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(state) }));
  await page.goto("/live");
  await expect(page.getByTestId("live-session-card")).toBeVisible();
}

const animation = (page: Page, testid: string) => page.getByTestId(testid).evaluate((el) => getComputedStyle(el).animationName);

test("live, key on while charging: the bolt and the glow pulse, and nothing moves", async ({ page }) => {
  await live(page, []);
  const card = page.getByTestId("live-session-card");
  const before = await card.boundingBox();
  const kw = await page.getByTestId("live-session-card").locator(".text-3xl").boundingBox();
  await expect(page.getByTestId("charge-pulse-bolt")).toHaveCount(0);
  await expect(page.getByTestId("charge-pulse-glow")).toHaveCount(0);
  const html = await card.innerHTML();

  await live(page, ["charge_pulse"]);
  await expect(page.getByTestId("charge-pulse-bolt")).toHaveAttribute("data-pulse", "charging");
  await expect(page.getByTestId("charge-pulse-glow")).toBeVisible();
  expect(await animation(page, "charge-pulse-bolt")).toBe("cp-pulse");
  expect(await animation(page, "charge-pulse-glow")).toBe("cp-pulse");
  const after = await page.getByTestId("live-session-card").boundingBox();
  expect(after?.height).toBe(before?.height);
  expect((await page.getByTestId("live-session-card").locator(".text-3xl").boundingBox())?.x).toBe(kw?.x);
  expect(html).not.toContain("charge-pulse");
  if (SHOTS) await page.getByTestId("live-session-card").screenshot({ path: path.join(SHOTS, "live-card-on.png") });
});

test("live, key on while plugged in and not charging: a still plug badge, no pulse", async ({ page }) => {
  await live(page, ["charge_pulse"], "parked", { charger_status: "chrgr_sts_connected_no_chrg", charger_state: "charging_ready", charger_power_kw: 0 });
  await expect(page.getByTestId("charge-pulse-bolt")).toHaveAttribute("data-pulse", "plugged");
  await expect(page.getByTestId("charge-pulse-glow")).toHaveCount(0);
  expect(await animation(page, "charge-pulse-bolt")).toBe("none");
});

test("live, key on, reduced motion: the glow stays, still", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await live(page, ["charge_pulse"]);
  await expect(page.getByTestId("charge-pulse-glow")).toBeVisible();
  await expect(page.getByTestId("charge-pulse-bolt")).toBeVisible();
  expect(await animation(page, "charge-pulse-glow")).toBe("none");
  expect(await animation(page, "charge-pulse-bolt")).toBe("none");
  expect(Number(await page.getByTestId("charge-pulse-glow").evaluate((el) => getComputedStyle(el).opacity))).toBeGreaterThan(0.5);
});

test("now card, key on while charging: the bolt beside the kW and the live dot pulse; off, neither", async ({ page }) => {
  const open = async (keys: string[]) => {
    await stubApi(page, { scenario: "charging", nowEndpoint: true, designPreview: [...keys], chargeCase: { elapsedMin: 90, soc: (m: number) => 40 + m * 0.136, kw: () => 11 } });
    await page.goto("/");
    await expect(page.getByTestId("now-charge-graph")).toBeVisible();
  };
  await open([]);
  await expect(page.getByTestId("charge-pulse-bolt")).toHaveCount(0);
  await expect(page.getByTestId("charge-pulse-dot")).toHaveCount(0);
  const slot = await page.getByTestId("now-slot-big").innerHTML();
  expect(slot).not.toContain("charge-pulse");

  await open(["charge_pulse"]);
  await expect(page.getByTestId("now-slot-big").getByTestId("charge-pulse-bolt")).toHaveAttribute("data-pulse", "charging");
  await expect(page.getByTestId("charge-pulse-dot")).toBeAttached();
  expect(await animation(page, "charge-pulse-dot")).toBe("cp-pulse");
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, "now-on.png") });
});
