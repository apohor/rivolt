import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Connecting to Rivian fails in a few ways, and each says what happened and what to do. Stubbed responses only: this
// never connects to Rivian.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
const j = (b: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(b) });

async function open(page: Page, rivian: "disconnected" | "mfa", answer: { status: number; body: unknown }, keys: string[] = []) {
  await stubApi(page, { rivian: rivian === "mfa" ? "disconnected" : rivian, designPreview: keys });
  const events: string[] = [];
  await page.route("**/api/events", (r) => {
    events.push(r.request().postData() ?? "");
    return r.fulfill(j({ ok: true }, 202));
  });
  if (rivian === "mfa") await page.route("**/api/settings/rivian/", (r) => r.fulfill(j({ enabled: true, authenticated: false, mfa_pending: true })));
  // The events switch is on for this server.
  await page.route("**/api/config", (r) =>
    r.fulfill(j({ features: { trip_planner_enabled: true, signup_enabled: true, events_enabled: true, design_preview: keys }, tiles: { url: "/api/maps/tiles/us.pmtiles", chargers_url: "/api/maps/tiles/chargers.pmtiles" }, valhalla: { path: "/api/maps/valhalla" }, impersonation: { disabled: true } })),
  );
  await page.route("**/api/settings/rivian/login", (r) => r.fulfill(j(answer.body, answer.status)));
  await page.route("**/api/settings/rivian/mfa", (r) => r.fulfill(j(answer.body, answer.status)));
  await page.goto("/onboarding");
  return events;
}
const fill = async (page: Page) => {
  await page.getByPlaceholder("you@example.com").fill("driver@example.invalid");
  await page.getByPlaceholder("Password").fill("not-a-real-password");
  await page.getByRole("button", { name: /^Sign in/ }).click();
};

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`a rejected login says what to check and offers the other account type, keeping the details · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, "disconnected", { status: 401, body: { error: "Rivian didn't accept that email or password. Double-check both", class: "user_action" } });
    await fill(page);
    const box = page.locator("[data-connect-failure='user_action']");
    await expect(box).toContainText("Rivian didn't accept that sign-in");
    await expect(box).toContainText("accept your invite");
    await expect(box).not.toContainText(/^Error:/);
    await expect(page.getByPlaceholder("you@example.com")).toHaveValue("driver@example.invalid");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include("[data-connect-failure]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await box.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `connect-failure-${v.n}-dark.png`) });
    await box.getByRole("button", { name: "Use my main account instead" }).click();
    await expect(page.getByRole("radio", { name: /main Rivian account/ })).toBeChecked();
    await ctx.close();
  });
}

test("Rivian down or rate limited: not the user's fault, and the upstream text never shows", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const a = await ctx.newPage();
  await open(a, "disconnected", { status: 503, body: { error: "graphql: INTERNAL_SERVER_ERROR at gateway", class: "outage" } });
  await fill(a);
  await expect(a.locator("[data-connect-failure='outage']")).toContainText("Rivian isn't answering right now");
  await expect(a.locator("[data-connect-failure='outage']").getByRole("button", { name: "Try again" })).toBeVisible();
  await expect(a.locator("[data-connect-failure='outage']").getByRole("button", { name: "I'll do this later" })).toBeVisible();
  await expect(a.locator("[data-connect-failure]")).not.toContainText(/graphql|gateway/i);
  const b = await ctx.newPage();
  await open(b, "disconnected", { status: 429, body: { error: "slow down", class: "rate_limited" } });
  await fill(b);
  await expect(b.locator("[data-connect-failure='rate_limited']")).toContainText("Too many tries right now");
  await ctx.close();
});

test("a wrong code says codes expire, and the failure is tracked with its class", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 320, height: 700 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const events = await open(page, "mfa", { status: 401, body: { error: "bad otp", class: "user_action" } });
  // The code row (input, Verify, Cancel) fits even at 320 (WebKit gives the input a wider default) with no sideways scroll.
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  const cancel = await page.getByRole("button", { name: "Cancel" }).boundingBox();
  expect(cancel!.x + cancel!.width).toBeLessThanOrEqual(320);
  await page.getByPlaceholder("123456").fill("123456");
  await page.getByRole("button", { name: "Verify" }).click();
  await expect(page.locator("[data-connect-failure]")).toContainText("That code didn't work");
  await expect(page.locator("[data-connect-failure]").getByRole("button", { name: "Enter code again" })).toBeVisible();
  await expect(page.locator("[data-connect-failure]").getByRole("button", { name: "Start again for a new code" })).toBeVisible();
  await expect.poll(() => events.join(" "), { timeout: 10_000 }).toContain('"class":"mfa"');
  await ctx.close();
});

// v1.4.1: Rivian answers an expired or superseded code with a server error, which arrives as class outage. The code step then says the code may
// have expired and offers a new one; it never says Rivian is down and never offers to send the same code again. Both forms (signup_v2 on and off).
for (const on of [true, false]) {
  test(`@webkit an outage on the code step says the code may have expired and offers a new code · key ${on ? "on" : "off"}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, "mfa", { status: 503, body: { error: "didn't accept this code", class: "outage" } }, on ? ["signup_v2"] : []);
    if (on) await page.getByLabel("Code").fill("123456");
    else await page.getByPlaceholder("123456").fill("123456");
    await page.getByRole("button", { name: on ? "Finish signing in" : "Verify" }).click();
    // The new form marks its failure box as an alert; the old one as data-connect-failure.
    const box = on ? page.getByRole("alert") : page.locator("[data-connect-failure]");
    await expect(box).toContainText("That code may have expired");
    await expect(box).toContainText("may have expired, or Rivian didn't answer");
    await expect(page.getByText("Rivian isn't answering")).toHaveCount(0);
    await expect(box.getByRole("button", { name: "Try again" })).toHaveCount(0);
    const again = box.getByRole("button", { name: on ? "Get a new code" : "Start again for a new code" });
    await expect(again).toBeVisible();
    await box.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `connect-code-outage-key-${on ? "on" : "off"}.png`) });
    // Starting again signs the half-made login out; the server then says there is no code to wait for.
    await page.route("**/api/settings/rivian/logout", (r) => r.fulfill(j({ authenticated: false })));
    await page.route("**/api/settings/rivian/", (r) => r.fulfill(j({ enabled: true, authenticated: false, mfa_pending: false })));
    await again.click();
    // This page opens straight on the code step (the stub says a code is pending), so the new form's way back is its "Add my truck" step, then the sign-in form.
    if (on) await page.getByRole("button", { name: /^Add my truck/ }).click();
    await expect(on ? page.getByLabel("Rivian email") : page.getByPlaceholder("you@example.com")).toBeVisible();
    await expect(on ? page.getByLabel("Code") : page.getByPlaceholder("123456")).toHaveCount(0);
    await ctx.close();
  });
}
