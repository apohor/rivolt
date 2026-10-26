import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Calendar scope: Day | Week | Month | Custom (with This year and All time inside it) with
// stepping; remembered per device and shared by Now, Activity and Truck.
// Flag off keeps the rolling window picker.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function open(page: import("@playwright/test").Page) {
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.theme", "dark");
  });
  await stubApi(page);
}

test("next: Day/Week/Month/Custom, This year and All time, stepping, persistence and sharing across pages", async ({ page }) => {
  await open(page);
  await page.goto("/");
  const scope = page.getByRole("group", { name: "Time scope" });
  await expect(scope).toBeVisible();
  // Default: this month, label like "Oct 2026"; next arrow disabled at the current period.
  await expect(scope.getByRole("button", { name: "Month", pressed: true })).toBeVisible();
  await expect(scope.getByRole("button", { name: "Next period" })).toBeDisabled();
  const monthLabel = await scope.locator("[aria-live]").innerText();
  expect(monthLabel).toMatch(/^[A-Z][a-z]{2} \d{4}$/);
  // Step back one month.
  await scope.getByRole("button", { name: "Previous period" }).click();
  await expect(scope.getByRole("button", { name: "Next period" })).toBeEnabled();
  expect(await scope.locator("[aria-live]").innerText()).not.toBe(monthLabel);
  // Week: label like "Oct 12–18" or "Sep 28–Oct 4".
  await scope.getByRole("button", { name: "Week" }).click();
  expect(await scope.locator("[aria-live]").innerText()).toMatch(/^[A-Z][a-z]{2} \d{1,2}–([A-Z][a-z]{2} )?\d{1,2}$/);
  // Day: "Today", then a previous day named like "Oct 24".
  await scope.getByRole("button", { name: "Day", exact: true }).click();
  expect(await scope.locator("[aria-live]").innerText()).toBe("Today");
  await expect(scope.getByRole("button", { name: "Next period" })).toBeDisabled();
  await scope.getByRole("button", { name: "Previous period" }).click();
  expect(await scope.locator("[aria-live]").innerText()).toMatch(/^[A-Z][a-z]{2} \d{1,2}(, \d{4})?$/);
  // Year and All are inside Custom: "This year" (a year label, stepping) and "All time" (no arrows); Custom shows the dates.
  await scope.getByRole("button", { name: "Custom" }).click();
  await scope.getByRole("button", { name: "This year" }).click();
  expect(await scope.locator("[aria-live]").innerText()).toMatch(/^\d{4}$/);
  await expect(scope.getByRole("button", { name: "Custom", pressed: true })).toBeVisible();
  await scope.getByRole("button", { name: "All time" }).click();
  expect(await scope.locator("[aria-live]").innerText()).toBe("All time");
  await expect(scope.getByRole("button", { name: "Previous period" })).toHaveCount(0);
  await scope.getByRole("button", { name: "Pick dates" }).click();
  await expect(scope.getByLabel("From")).toBeVisible();
  await expect(scope.getByLabel("To")).toBeVisible();
  await scope.getByLabel("From").fill("2026-10-03");
  await scope.getByLabel("To").fill("2026-10-17");
  // Remembered per device and shared: reload, then Activity and Truck show it too.
  await page.reload();
  await expect(page.getByRole("group", { name: "Time scope" }).getByLabel("From")).toHaveValue("2026-10-03");
  await page.goto("/activity");
  await expect(page.getByRole("group", { name: "Time scope" }).getByLabel("To")).toHaveValue("2026-10-17");
  await page.goto("/truck");
  await expect(page.getByRole("group", { name: "Time scope" }).getByLabel("From")).toHaveValue("2026-10-03");
});

test("next: a sleep-activity request carries the period's since and until", async ({ page }) => {
  await open(page);
  const urls: string[] = [];
  page.on("request", (r) => { if (r.url().includes("/api/sleep-activity")) urls.push(r.url()); });
  await page.goto("/");
  await page.getByRole("group", { name: "Time scope" }).getByRole("button", { name: "Week" }).click();
  await expect.poll(() => urls.some((u) => u.includes("since=") && u.includes("until="))).toBe(true);
});

for (const v of [
  { name: "iphone", vp: { width: 390, height: 844 } },
  { name: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`next: scope screenshots and axe · ${v.name} · dark`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page);
    await page.addInitScript(() =>
      localStorage.setItem("rivolt.period", JSON.stringify({ kind: "custom", offset: 0, from: "2026-10-03", to: "2026-10-17" })),
    );
    await page.goto("/");
    await expect(page.getByRole("group", { name: "Time scope" })).toBeVisible();
    await page.waitForTimeout(800);
    await page.screenshot({ path: path.join(SHOTS, `uinext-period-custom-${v.name}.png`) });
    // On a phone the choices live behind the compact label.
    if (v.name === "iphone") await page.getByRole("group", { name: "Time scope" }).getByRole("button", { name: /^Time scope:/ }).click();
    await page.getByRole("group", { name: "Time scope" }).getByRole("button", { name: "Week" }).click();
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SHOTS, `uinext-period-week-${v.name}.png`) });
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}

test("next desktop: the hero card and the KPI grid share top and bottom edges; the scope sits in the page header", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page);
  await page.goto("/");
  const hero = page.locator("[data-ui-hero]").first();
  const kpi = page.locator("[data-ui-area='kpi']");
  await expect(hero).toBeVisible();
  const a = (await hero.boundingBox())!;
  const b = (await kpi.boundingBox())!;
  expect(Math.abs(a.y - b.y)).toBeLessThanOrEqual(2);
  expect(Math.abs(a.y + a.height - (b.y + b.height))).toBeLessThanOrEqual(2);
  // The scope control lives in the header row with the title.
  const header = page.locator("[data-ui-header]").first();
  await expect(header.getByRole("group", { name: "Time scope" })).toBeVisible();
  await expect(header.getByRole("heading", { name: "Now" })).toBeVisible();
  await page.screenshot({ path: path.join(SHOTS, "uinext-now-parity-desktop.png") });
  await ctx.close();
});

for (const url of ["/", "/activity", "/truck"]) {
  test(`next: the scope header is sticky under the nav on scroll · ${url}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 600 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page);
    // All time: enough rows to scroll under the nav whatever the date (in a month's
    // first minutes "this month" has almost nothing on /activity).
    await page.addInitScript(() => localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 })));
    await page.goto(url);
    const header = page.locator("[data-ui-header]").first();
    await expect(header).toBeVisible();
    // The shell paints before the page's cards load (on /truck they arrive one by one), so wait until there is
    // room to scroll the header under the nav; a wheel on a still-short page left it in place (y 119 on CI).
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight)).toBeGreaterThan(300);
    await page.mouse.wheel(0, 900);
    // Pinned just under the 48px top nav, still on screen after scrolling.
    await expect
      .poll(async () => {
        const y = (await header.boundingBox())!.y;
        return y >= 40 && y <= 60 ? "pinned" : `y ${y}`;
      })
      .toBe("pinned");
    await ctx.close();
  });
}

test("next phone: the open choices sit above the page's own tabs and never cover them", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page);
  await page.goto("/activity");
  const scope = page.getByRole("group", { name: "Time scope" });
  await scope.getByRole("button", { name: /^Time scope:/ }).click();
  const panel = page.locator("[data-period-panel]");
  await expect(panel).toBeVisible();
  const charges = page.getByRole("tab", { name: "Charges" });
  await expect(charges).toBeVisible();
  const [p, c] = await Promise.all([panel.boundingBox(), charges.boundingBox()]);
  // The tab starts below the panel: nothing is hidden while the choices are open.
  expect(c!.y).toBeGreaterThanOrEqual(p!.y + p!.height - 1);
  await ctx.close();
});

test("next phone: a compact scope bar; tapping the label opens the choices", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page);
  await page.goto("/");
  const scope = page.getByRole("group", { name: "Time scope" });
  // No full segmented row inline on a phone.
  await expect(scope.getByRole("button", { name: "Week" })).toBeHidden();
  const label = scope.getByRole("button", { name: /^Time scope:/ });
  await expect(label).toBeVisible();
  await label.click();
  await expect(scope.getByRole("button", { name: "Week" })).toBeVisible();
  await page.screenshot({ path: path.join(SHOTS, "uinext-period-compact-open-iphone.png") });
  await scope.getByRole("button", { name: "Week" }).click();
  await expect(label).toHaveText(/^[A-Z][a-z]{2} \d{1,2}–/);
  await page.keyboard.press("Escape");
  await expect(scope.getByRole("button", { name: "Week" })).toBeHidden();
  await ctx.close();
});
