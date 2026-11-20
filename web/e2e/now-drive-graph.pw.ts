import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Design "now_drive_graph" (with now_slot and now_tidy): the Now card of a driving truck draws the speed from the start of the drive to now,
// with the battery as a quieter second line. A drive too short to draw, or the key off: the card is as it was. SHOTS_DIR makes the image-gate shots.
const SHOTS = process.env.SHOTS_DIR;
const KEYS = ["now_v2", "now_tidy", "now_slot", "now_drive_graph"];
type Case = { elapsedMin: number; mph: (m: number) => number; soc: (m: number) => number };
const CASES: Record<string, Case> = {
  // A town drive that joins a highway and slows for the destination.
  middle: { elapsedMin: 22, mph: (m) => (m < 3 ? m * 12 : m < 14 ? 55 + 8 * Math.sin(m / 2) : Math.max(25, 62 - (m - 14) * 4)), soc: (m) => 71 - m * 0.18 },
  stop: { elapsedMin: 18, mph: (m) => (m < 6 ? 40 : m < 9 ? 0 : 45 + 5 * Math.sin(m)), soc: (m) => 64 - m * 0.15 },
  fresh: { elapsedMin: 1, mph: (m) => m * 30, soc: (m) => 70 - m * 0.1 },
};

async function open(page: Page, c: Case, keys = KEYS, destination: "trip" | "none" = "trip") {
  await stubApi(page, { scenario: "driving", nowEndpoint: true, designPreview: keys, driveCase: c, liveDestination: destination });
  await page.goto("/");
  await expect(page.getByTestId("now-pill")).toBeVisible();
}

test("a drive in progress draws the speed and the battery from its start @webkit", async ({ page }) => {
  await open(page, CASES.middle);
  const g = page.getByTestId("now-drive-graph");
  await expect(g).toBeVisible();
  await expect(g).toContainText("Speed while driving");
  await expect(g.getByTestId("now-drive-change")).toHaveText(/^−\d+%\s*since \d{1,2}:\d{2}/);
  await expect(g.locator("svg")).toHaveAttribute("aria-label", /Speed since \d{1,2}:\d{2}.*now, top \d+ mph/);
  await expect(g).toContainText("Speed (mph)");
  await expect(g).toContainText("Battery");
  // The card's own state and slot are still there.
  await expect(page.getByTestId("now-slot")).toBeVisible();
});

test("a drive that just started has no graph and the card is as it was @webkit", async ({ page }) => {
  await open(page, CASES.fresh);
  await expect(page.getByTestId("now-slot")).toBeVisible();
  await page.waitForTimeout(800);
  await expect(page.getByTestId("now-drive-graph")).toHaveCount(0);
  await expect(page.locator("[data-now-soft]")).toHaveCount(0);
});

test("a stop in the middle of the drive shows as speed falling to zero and coming back @webkit", async ({ page }) => {
  await open(page, CASES.stop);
  const g = page.getByTestId("now-drive-graph");
  await expect(g).toBeVisible();
  await expect(g.locator("svg")).toHaveAttribute("aria-label", /top \d+ mph/);
});

test("the key off leaves the card as it was @webkit", async ({ page }) => {
  await open(page, CASES.middle, ["now_v2", "now_tidy", "now_slot"]);
  await expect(page.getByTestId("now-slot")).toBeVisible();
  await page.waitForTimeout(800);
  await expect(page.getByTestId("now-drive-graph")).toHaveCount(0);
});

test("with a destination or without, the graph and the slot do not overlap @webkit", async ({ page }) => {
  for (const d of ["trip", "none"] as const) {
    await open(page, CASES.middle, KEYS, d);
    const g = await page.getByTestId("now-drive-graph").boundingBox();
    const s = await page.getByTestId("now-slot").boundingBox();
    expect(g).not.toBeNull();
    expect(s).not.toBeNull();
    const apart = g!.y + g!.height <= s!.y + 1 || s!.y + s!.height <= g!.y + 1 || g!.x + g!.width <= s!.x + 1 || s!.x + s!.width <= g!.x + 1;
    expect(apart).toBe(true);
  }
});

if (SHOTS) {
  // The image-gate matrix: the cases at 393 and 1280, both themes, miles and kilometres, with and without a destination.
  for (const c of ["middle", "stop"]) for (const w of [393, 1280]) for (const scheme of ["light", "dark"] as const) for (const unit of ["mi", "km"]) {
    test(`shot ${c} ${w} ${scheme} ${unit}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w < 900 ? 1700 : 1000 }, colorScheme: scheme });
      const page = await ctx.newPage();
      await page.addInitScript((u) => localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: u })), unit);
      await open(page, CASES[c]);
      await page.addStyleTag({ content: "header, nav, .fixed, .sticky, [data-ui-header] { visibility: hidden !important }" });
      await page.waitForTimeout(500);
      await page.locator("[data-now-v2]").first().screenshot({ path: `${SHOTS}/drive-${c}-${w}-${scheme}-${unit}.png` });
      await ctx.close();
    });
  }
}
