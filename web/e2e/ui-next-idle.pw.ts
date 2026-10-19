import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// ui_theme_next: Now drops the idle-awake chart for a compact tile (today, average,
// 7-day sparkline) linking to Truck's full sleep chart; the wake warning stays.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    for (const state of ["off", "on"] as const) {
      test(`idle tile ${state} · ${v.name} · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript(([s, t]) => {
          localStorage.setItem("rivolt.ui.next", s);
          localStorage.setItem("rivolt.theme", t);
        }, [state, theme]);
        await stubApi(page, { scenario: "parked", drain: "ok" });
        await page.goto("/");
        await expect(page.getByRole("region", { name: "Now" })).toBeVisible();
        await page.waitForTimeout(1200);
        if (state === "off") {
          await expect(page.getByTestId("idle-tile")).toHaveCount(0);
          await expect(page.getByRole("heading", { name: "Idle-awake time per day" })).toBeVisible();
        } else {
          const tile = page.getByTestId("idle-tile");
          await expect(tile).toBeVisible();
          await expect(tile).toContainText("Idle awake");
          await expect(tile).toContainText(/today · avg /);
          await expect(page.getByRole("heading", { name: "Idle-awake time per day" })).toHaveCount(0);
          await expect(page.getByRole("status").filter({ hasText: "Not staying asleep" })).toBeVisible();
        }
        await page.getByTestId("idle-tile").or(page.getByText("Idle-awake time per day").first()).first().scrollIntoViewIfNeeded();
        await page.screenshot({ path: path.join(SHOTS, `idle-${v.name}-${theme}-${state}.png`), fullPage: true });
        if (state === "on") {
          const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
          const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
          expect(summary, summary.join("\n")).toEqual([]);
        }
        await ctx.close();
      });
    }
  }
}

test("the tile opens Truck's sleep card", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page, { scenario: "parked", drain: "ok" });
  await page.goto("/");
  await page.getByTestId("idle-tile").click();
  await expect(page).toHaveURL(/\/truck#sleep$/);
  await expect(page.locator("#sleep").getByText("Sleep and idle-awake")).toBeVisible();
});

// qa2 (iPhone, prod): "Idle awake 3 h 37 min today" wrapped onto three lines. At 390px the
// value stays on one line and the whole tile is at most two text lines of 11/12/14/24.
for (const theme of ["dark", "light"] as const) {
  test(`idle tile with a long value stays tidy at 390 · ${theme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => {
      localStorage.setItem("rivolt.ui.next", "on");
      localStorage.setItem("rivolt.theme", t);
    }, [theme]);
    await stubApi(page, { scenario: "parked", drain: "ok" });
    await page.route("**/api/sleep-activity**", (r) => {
      const days = Array.from({ length: 14 }, (_, i) => ({
        day: new Date(e2eNow() - (13 - i) * 86_400_000).toISOString().slice(0, 10) + "T00:00:00Z",
        asleep_h: 18, idle_awake_h: i === 13 ? 3.62 : 1.43, sleeps: 3, failed_sleeps: 0,
      }));
      return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(days) });
    });
    await page.goto("/");
    const tile = page.getByTestId("idle-tile");
    await expect(tile).toContainText("3 h 37 min");
    await page.waitForTimeout(500);
    const res = await tile.evaluate((el) => {
      const lines = [...el.querySelectorAll("div")].map((d) => {
        const cs = getComputedStyle(d);
        const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.5;
        return { t: (d.textContent || "").trim(), lines: Math.round(d.getBoundingClientRect().height / lh), size: Math.round(parseFloat(cs.fontSize)) };
      });
      return { lines, over: el.scrollWidth > el.clientWidth + 1 };
    });
    expect(res.over).toBe(false);
    for (const l of res.lines) {
      expect(l.lines, l.t).toBe(1);
      expect([11, 12, 14, 24, 36], `${l.t} size`).toContain(l.size);
    }
    await tile.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `idle-tile-long-390-${theme}.png`) });
    await ctx.close();
  });
}

test("a new truck's idle tile keeps the caption: today · no full days yet, on one line at 390", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page, { scenario: "parked", drain: "ok" });
  await page.route("**/api/sleep-activity**", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ day: new Date(e2eNow()).toISOString().slice(0, 10) + "T00:00:00Z", asleep_h: 10, idle_awake_h: 1.1, sleeps: 1, failed_sleeps: 0 }]) }),
  );
  await page.goto("/");
  const tile = page.getByTestId("idle-tile");
  await expect(tile).toContainText("today · no full days yet");
  const lines = await tile.evaluate((el) => [...el.querySelectorAll("div")].map((d) => Math.round(d.getBoundingClientRect().height / (parseFloat(getComputedStyle(d).lineHeight) || parseFloat(getComputedStyle(d).fontSize) * 1.5))));
  for (const n of lines) expect(n).toBe(1);
  await ctx.close();
});
