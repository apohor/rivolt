import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Settings → Notifications: the plug-in reminder and milestones (per device, off by default), the
// reminder's level and time (account-wide), the weekly recap, and the browser time zone on every save.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

type Body = Record<string, unknown>;

async function open(page: Page, opts: { off?: number; putStatus?: number; home?: boolean } = {}) {
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.ui.next", "on");
    // A device that is already subscribed: the panel reads its switches back from the server.
    const sub = { endpoint: "https://push.test/device-1", toJSON: () => ({ endpoint: "https://push.test/device-1", keys: { p256dh: "a", auth: "b" } }), unsubscribe: async () => true };
    const reg = { pushManager: { getSubscription: async () => sub, subscribe: async () => sub } };
    Object.defineProperty(navigator.serviceWorker, "ready", { get: () => Promise.resolve(reg) });
    Object.defineProperty(Notification, "permission", { get: () => "granted" });
  });
  await stubApi(page);
  const subscribes: Body[] = [];
  const puts: Body[] = [];
  let eng = { timezone: "", timezone_stored: false, weekly_recap: false, plug_threshold_pct: 40, plug_remind_at: "20:00" };
  const json = (body: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.route("**/api/push/status", (r) => r.fulfill(json({ enabled: true, subscription_count: 1 })));
  await page.route("**/api/push/vapid-key", (r) => r.fulfill(json({ public_key: "BAAA" })));
  await page.route("**/api/push/device", (r) =>
    r.fulfill(json({ preferences: { on_charging_done: true, on_plug_in_reminder: false, on_anomaly: true, on_software_update: true, on_milestones: false } })),
  );
  await page.route("**/api/push/subscribe", (r) => {
    const b = r.request().postDataJSON() as { preferences: Body };
    subscribes.push(b.preferences);
    return r.fulfill(json({ ok: true, preferences: b.preferences }));
  });
  const optins: Body[] = [];
  await page.route("**/api/push/optin", (r) => {
    optins.push(r.request().postDataJSON() as Body);
    return r.fulfill(json({ ok: true }));
  });
  const offs: Body[] = [];
  await page.route("**/api/push/off", (r) => {
    offs.push(r.request().postDataJSON() as Body);
    return opts.off === 200 ? r.fulfill(json({ ok: true, devices_changed: 2 })) : r.fulfill(json({ error: "not found" }, 404));
  });
  await page.route("**/api/settings/home-location", (r) => r.fulfill(json({ set: !!opts.home, latitude: opts.home ? 30 : 0, longitude: opts.home ? -97 : 0, label: "Home" })));
  await page.route("**/api/settings/engagement", (r) => {
    if (r.request().method() === "GET") return r.fulfill(json(eng));
    if (opts.putStatus) return r.fulfill({ status: opts.putStatus, contentType: "text/plain", body: "timezone is not a zone" });
    const b = r.request().postDataJSON() as Body;
    puts.push(b);
    eng = { ...eng, ...(b as object), timezone_stored: true } as typeof eng;
    return r.fulfill(json(eng));
  });
  await page.goto("/settings?tab=notifications");
  await expect(page.getByText("Subscribed on this device")).toBeVisible();
  return { subscribes, puts, offs, optins };
}

const plug = (page: Page) => page.getByRole("checkbox", { name: /Plug-in reminder/ });
const milestones = (page: Page) => page.getByRole("checkbox", { name: /Milestones/ });

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`reminder and milestones start off, turn on with the zone, and set their level and time · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark", timezoneId: "America/Chicago" });
    const page = await ctx.newPage();
    const { subscribes, puts, optins } = await open(page);
    // Opt-in: off, as the server has them, until the user turns them on.
    await expect(plug(page)).not.toBeChecked();
    await expect(milestones(page)).not.toBeChecked();
    await expect(page.locator("[data-plug-reminder]")).toHaveCount(0);

    await plug(page).check();
    await expect(page.locator("[data-plug-reminder]")).toBeVisible();
    // The reminder goes through its own opt-in call, and the browser's zone is stored so it can ever fire.
    await expect.poll(() => optins.at(-1)).toEqual({ endpoint: "https://push.test/device-1", on_plug_in_reminder: true });
    await expect.poll(() => puts.at(-1)).toEqual({ timezone: "America/Chicago" });
    // A re-save of the other switches sends all five and cannot turn the reminder on by itself.
    await page.getByRole("checkbox", { name: "Software updates" }).uncheck();
    await expect.poll(() => subscribes.at(-1)).toMatchObject({ on_software_update: false });
    expect(Object.keys(subscribes.at(-1)!).sort()).toEqual(["on_anomaly", "on_charging_done", "on_milestones", "on_plug_in_reminder", "on_software_update"]);
    // No home place saved: say where the reminder fires.
    await expect(page.locator("[data-plug-reminder]")).toContainText("Set your home place in Settings → Places first");

    // Level and time are account-wide and carry the zone too.
    const level = page.getByLabel("Remind me below");
    await level.fill("30");
    await level.blur();
    await expect.poll(() => puts.at(-1)).toEqual({ plug_threshold_pct: 30, timezone: "America/Chicago" });
    const at = page.getByLabel("At", { exact: true });
    await at.fill("21:30");
    await at.blur();
    await expect.poll(() => puts.at(-1)).toEqual({ plug_remind_at: "21:30", timezone: "America/Chicago" });
    // A time the server would refuse is held back with a plain sentence.
    const n = puts.length;
    await at.fill("23:00");
    await at.blur();
    await expect(page.getByRole("alert")).toContainText("no later than 10:00 PM");
    expect(puts.length).toBe(n);
    await level.fill("99");
    await level.blur();
    await expect(page.getByRole("alert")).toContainText("5% to 95%");
    expect(puts.length).toBe(n);

    await milestones(page).check();
    await expect.poll(() => optins.at(-1)).toEqual({ endpoint: "https://push.test/device-1", on_milestones: true });

    await page.getByRole("switch", { name: "Weekly recap by email" }).click();
    await expect.poll(() => puts.at(-1)).toEqual({ weekly_recap: true, timezone: "America/Chicago" });
    await expect(page.getByText(/Skipped on weeks with no driving\. Turn off any time\./)).toBeVisible();
    await expect(page.getByText(/haven.t visited/)).toHaveCount(0);
    await page.waitForTimeout(600);
    await page.screenshot({ path: path.join(SHOTS, `alerts-${v.n}-dark.png`), fullPage: true });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((t) => t.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}

test("with a home place there is no home note; Stop these reminders turns it off on every device when the server can", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark", timezoneId: "America/Chicago" });
  const page = await ctx.newPage();
  const { offs } = await open(page, { off: 200, home: true });
  await plug(page).check();
  await expect(page.locator("[data-plug-reminder]")).toBeVisible();
  await expect(page.locator("[data-plug-reminder]")).not.toContainText("Set your home place");
  await page.getByRole("button", { name: "Stop these reminders" }).click();
  await expect(page.getByText("Turned off on all your devices.")).toBeVisible();
  expect(offs).toEqual([{ kind: "plug_in_reminder" }]);
  await expect(plug(page)).not.toBeChecked();
  await ctx.close();
});

test("until the server can stop reminders everywhere, the button says it only stopped this device", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark", timezoneId: "America/Chicago" });
  const page = await ctx.newPage();
  const { optins } = await open(page, { home: true });
  await plug(page).check();
  await page.getByRole("button", { name: "Stop these reminders" }).click();
  await expect(page.getByText("Turned off on this device. Other devices keep it until you turn it off there.")).toBeVisible();
  await expect(plug(page)).not.toBeChecked();
  await expect.poll(() => optins.at(-1)).toEqual({ endpoint: "https://push.test/device-1", on_plug_in_reminder: false });
  await ctx.close();
});

test("if the zone cannot be saved, the reminder is not left on and the reason shows", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark", timezoneId: "America/Chicago" });
  const page = await ctx.newPage();
  const { optins } = await open(page, { putStatus: 400 });
  await plug(page).check();
  await expect(page.getByText(/Couldn't save your time zone, so this reminder was left off/)).toBeVisible();
  await expect(plug(page)).not.toBeChecked();
  // The opt-in is taken back, so the server does not keep a reminder that can never fire.
  await expect.poll(() => optins.at(-1)).toEqual({ endpoint: "https://push.test/device-1", on_plug_in_reminder: false });
  await ctx.close();
});
