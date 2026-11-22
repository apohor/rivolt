import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Design "now_drive_graph" (with now_slot and now_tidy): the Now card of a driving truck draws the speed from the start of the drive to now,
// with the battery as a quieter second line. A drive too short to draw, or the key off: the card is as it was. SHOTS_DIR makes the image-gate shots.
const SHOTS = process.env.SHOTS_DIR;
const KEYS = [];
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
  await expect(g).toContainText("This drive");
  await expect(g.getByTestId("now-drive-change")).toHaveText(/^−\d+%\s*since \d{1,2}:\d{2}/);
  await expect(g.locator("svg")).toHaveAttribute("aria-label", /Speed since \d{1,2}:\d{2}.*now, top \d+ mph/);
  await expect(g.getByTestId("now-drive-top")).toHaveText(/^\d+ mph$/);
  await expect(g).toContainText("Speed (mph)");
  await expect(g).toContainText("Battery");
  // The card's own state and slot are still there.
  await expect(page.getByTestId("now-slot")).toBeVisible();
});

test("a drive that just started has the frame with a short state, and the card keeps its height @webkit", async ({ page }) => {
  await open(page, CASES.fresh);
  await expect(page.getByTestId("now-slot")).toBeVisible();
  const g = page.getByTestId("now-drive-graph");
  await expect(g).toBeVisible();
  await expect(g.getByTestId("now-drive-just-started")).toHaveText("Drive just started");
  await expect(g.getByTestId("now-drive-change-since")).toHaveText(/^since \d{1,2}:\d{2}/);
  await expect(g.locator("svg")).toHaveCount(0);
  const early = (await g.boundingBox())!.height;
  await open(page, CASES.middle);
  await expect(page.getByTestId("now-drive-top")).toBeVisible();
  const later = (await page.getByTestId("now-drive-graph").boundingBox())!.height;
  expect(Math.abs(later - early), `card ${early} px early, ${later} px with the line`).toBeLessThanOrEqual(2);
});

test("a stop in the middle of the drive shows as speed falling to zero and coming back @webkit", async ({ page }) => {
  await open(page, CASES.stop);
  const g = page.getByTestId("now-drive-graph");
  await expect(g).toBeVisible();
  await expect(g.locator("svg")).toHaveAttribute("aria-label", /top \d+ mph/);
});

test("with a destination or without, the graph and the slot do not overlap @webkit", async ({ page }) => {
  for (const d of ["trip", "none"] as const) {
    await open(page, CASES.middle, KEYS, d);
    // Read both boxes together until they are there (the card remounts its graph once as the state settles, and a box read in between is null).
    await expect
      .poll(async () => {
        const g = await page.getByTestId("now-drive-graph").boundingBox();
        const s = await page.getByTestId("now-slot").boundingBox();
        if (!g || !s) return "not drawn";
        const apart = g.y + g.height <= s.y + 1 || s.y + s.height <= g.y + 1 || g.x + g.width <= s.x + 1 || s.x + s.width <= g.x + 1;
        return apart ? "apart" : "overlap";
      })
      .toBe("apart");
  }
});

test("the destination is said near the header only when the trip has one, and Cabin and Outside both show @webkit", async ({ page }) => {
  await open(page, CASES.middle, KEYS, "trip");
  await expect(page.getByTestId("now-drive-dest")).toHaveText(/^12 mi to Test Depot · arrive \d{1,2}:\d{2}/);
  await expect(page.getByTestId("now-cond-cabin")).toBeVisible();
  await expect(page.getByTestId("now-cond-outside")).toBeVisible();
  await open(page, CASES.middle, KEYS, "none");
  await expect(page.getByTestId("now-drive-graph")).toBeVisible();
  await expect(page.getByTestId("now-drive-dest")).toHaveCount(0);
});

if (SHOTS) {
  // The image-gate matrix: the cases at 393 and 1280, both themes, miles and kilometres, with and without a destination.
  for (const c of ["middle", "stop"]) for (const w of [393, 1280, 1440]) for (const scheme of ["light", "dark"] as const) for (const unit of ["mi", "km"]) {
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

// The top-speed label's outline is the card's own surface colour in both themes, set by the app's theme (not the device's): never white on a dark card.
for (const theme of ["dark", "light"] as const) {
  for (const device of ["dark", "light"] as const) {
    test(`the top label's outline follows the ${theme} app theme on a ${device} device @webkit`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: 393, height: 900 }, colorScheme: device });
      const page = await ctx.newPage();
      await open(page, CASES.middle);
      await page.evaluate((t) => (t === "light" ? document.documentElement.setAttribute("data-theme", "light") : document.documentElement.removeAttribute("data-theme")), theme);
      const got = await page.getByTestId("now-drive-top").evaluate((el) => {
        const probe = document.createElement("div");
        probe.style.color = "var(--color-neutral-900)";
        document.body.appendChild(probe);
        const surface = getComputedStyle(probe).color;
        probe.remove();
        return { stroke: getComputedStyle(el).stroke, surface };
      });
      expect(got.stroke).toBe(got.surface);
      if (theme === "dark") expect(got.stroke).not.toBe("rgb(255, 255, 255)");
      await ctx.close();
    });
  }
}
