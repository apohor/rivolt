import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Settings → Security → Signed-in devices: the browsers and the iPhone app signed in as this user, "This device", one sign-out
// with a confirm, "Sign out everywhere else", the 409 note, and nothing at all where the server has no sessions (404) or an
// admin is viewing as someone (403).
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
const sess = (id: string, kind: "web" | "app", label: string, o: Record<string, unknown> = {}) => ({
  id, kind, device_label: label, created_at: ago(60 * 24 * 3), last_seen_at: ago(5), expires_at: ago(-60 * 24 * 27), current: false, ...o,
});
const THREE = () => [
  sess("11111111-1111-4111-8111-111111111111", "web", "Chrome on macOS", { current: true, last_seen_at: ago(0) }),
  sess("22222222-2222-4222-8222-222222222222", "web", "Safari on iOS", { last_seen_at: ago(60 * 30) }),
  sess("33333333-3333-4333-8333-333333333333", "app", "iPhone", { last_seen_at: ago(12) }),
];

async function open(page: Page, o: { list?: object[] | number; revokeOthers?: number } = {}) {
  await stubApi(page, { scenario: "parked" });
  const calls: { method: string; url: string }[] = [];
  let rows = Array.isArray(o.list) ? [...o.list] : THREE();
  const json = (body: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.route("**/api/auth/sessions**", (r) => {
    const req = r.request();
    const u = new URL(req.url());
    calls.push({ method: req.method(), url: u.pathname });
    if (typeof o.list === "number") return r.fulfill(json({ error: "nope" }, o.list));
    if (req.method() === "GET") return r.fulfill(json({ sessions: rows }));
    if (u.pathname.endsWith("/revoke-others")) {
      if (o.revokeOthers) return r.fulfill(json({ error: "this sign-in has no session to keep" }, o.revokeOthers));
      const n = rows.filter((x) => !(x as { current: boolean }).current).length;
      rows = rows.filter((x) => (x as { current: boolean }).current);
      return r.fulfill(json({ revoked: n }));
    }
    const id = u.pathname.split("/").pop();
    rows = rows.filter((x) => (x as { id: string }).id !== id);
    return r.fulfill({ status: 204 });
  });
  await page.goto("/settings?tab=security");
  return calls;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`lists the devices, marks this one, and signs another out after a confirm · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const calls = await open(page);
    const card = page.locator("[data-signed-in-devices]");
    await expect(card.locator("[data-device-row]")).toHaveCount(3);
    await expect(card).toContainText("Chrome on macOS");
    await expect(card).toContainText("iPhone");
    await expect(card.locator("[data-device-current]")).toHaveCount(1);
    await expect(card.locator("[data-device-row]").first()).toContainText("This device");
    // No address, no place, no raw user agent.
    await expect(card).not.toContainText(/\d+\.\d+\.\d+\.\d+/);
    await page.waitForTimeout(300);
    await page.screenshot({ path: path.join(SHOTS, `signed-in-devices-${v.n}.png`), fullPage: true });
    // One sign-out asks first, then calls DELETE for that session and the row goes.
    await page.getByRole("button", { name: "Sign out Safari on iOS" }).click();
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(0);
    await page.getByRole("button", { name: "Cancel" }).first().click();
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(0);
    await page.getByRole("button", { name: "Sign out Safari on iOS" }).click();
    await page.getByRole("button", { name: "Yes, sign out" }).click();
    await expect(card.locator("[data-device-row]")).toHaveCount(2);
    expect(calls.filter((c) => c.method === "DELETE").map((c) => c.url)).toEqual(["/api/auth/sessions/22222222-2222-4222-8222-222222222222"]);
    const res = await new AxeBuilder({ page }).include("[data-signed-in-devices]").withTags(["wcag2a", "wcag2aa"]).analyze();
    expect(res.violations.map((x) => x.id)).toEqual([]);
    await ctx.close();
  });
}

test("sign out everywhere else asks first, then leaves only this device", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const calls = await open(page);
  const card = page.locator("[data-signed-in-devices]");
  await page.getByRole("button", { name: "Sign out everywhere else" }).click();
  expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  await page.getByRole("button", { name: "Yes, sign out 2 other devices" }).click();
  await expect(card.locator("[data-devices-note]")).toContainText("Signed out 2 other devices");
  await expect(card.locator("[data-device-row]")).toHaveCount(1);
  await expect(page.getByRole("button", { name: "Sign out everywhere else" })).toHaveCount(0);
  await ctx.close();
});

test("a sign-in with no session to keep says so instead of failing silently", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  // No row is marked current (a header sign-in): the button explains why it can't run.
  await open(page, { list: THREE().map((s) => ({ ...s, current: false })) });
  await page.getByRole("button", { name: "Sign out everywhere else" }).click();
  await expect(page.locator("[data-devices-note]")).toContainText("no session to keep");
  await ctx.close();
});

test("the server's 409 on sign out everywhere else is shown as a note", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, { revokeOthers: 409 });
  await page.getByRole("button", { name: "Sign out everywhere else" }).click();
  await page.getByRole("button", { name: /Yes, sign out 2 other devices/ }).click();
  await expect(page.locator("[data-devices-note]")).toContainText("no session to keep");
  await ctx.close();
});

test("signing out this device goes to the login page", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const calls = await open(page);
  await page.getByRole("button", { name: "Sign out Chrome on macOS" }).click();
  await expect(page.getByRole("button", { name: "Sign out this device" })).toBeVisible();
  await page.route("**/login", (r) => r.fulfill({ status: 200, contentType: "text/html", body: "<html><body>login</body></html>" }));
  await Promise.all([page.waitForURL("**/login"), page.getByRole("button", { name: "Sign out this device" }).click()]);
  expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(1);
  await ctx.close();
});

for (const status of [404, 403]) {
  test(`no card at all when the server answers ${status}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, { list: status });
    await expect(page.locator("[data-security-form=password]")).toBeVisible(); // the Security tab rendered
    await expect(page.locator("[data-signed-in-devices]")).toHaveCount(0);
    await expect(page.getByText("Signed-in devices")).toHaveCount(0);
    await ctx.close();
  });
}
