import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import path from "node:path";
import { stubApi, VEHICLE_ID } from "./stub";

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");

// The "Update alerts" row in the first-day card. Chromium can't reach a real push service, so the
// Push API is faked: the page sees a service worker with a pushManager, and a Notification whose
// permission and prompt result the test picks.
async function open(page: Page, o: { permission?: "default" | "denied" | "granted"; prompt?: "granted" | "denied"; pushEnabled?: boolean; existing?: boolean; checkDelayMs?: number } = {}) {
  const { permission = "default", prompt = "granted", pushEnabled = true, existing = false, checkDelayMs = 0 } = o;
  await page.addInitScript(
    ([perm, result, hasSub, delay]) => {
      const w = window as unknown as Record<string, unknown>;
      w.__prompts = 0;
      let sub: unknown = hasSub
        ? { toJSON: () => ({ endpoint: "https://push.example/0", keys: { p256dh: "p", auth: "a" } }), endpoint: "https://push.example/0" }
        : null;
      const reg = {
        pushManager: {
          getSubscription: async () => {
            await new Promise((r) => setTimeout(r, delay as number));
            return sub;
          },
          subscribe: async () => {
            sub = { toJSON: () => ({ endpoint: "https://push.example/1", keys: { p256dh: "p", auth: "a" } }), endpoint: "https://push.example/1" };
            return sub;
          },
        },
      };
      Object.defineProperty(navigator, "serviceWorker", {
        value: { ready: Promise.resolve(reg), register: async () => reg, getRegistration: async () => reg },
        configurable: true,
      });
      let current = perm;
      const N = function () {} as unknown as { permission: string; requestPermission: () => Promise<string> };
      Object.defineProperty(N, "permission", { get: () => current });
      N.requestPermission = async () => {
        (w.__prompts as number)++;
        current = result;
        return result;
      };
      w.Notification = N;
      w.PushManager = function () {};
    },
    [permission, prompt, existing, checkDelayMs] as const,
  );
  await stubApi(page);
  await page.route("**/api/drives*", (r) => (r.request().url().includes("/api/drives/") ? r.fallback() : r.fulfill({ json: [] })));
  await page.route("**/api/charges*", (r) => (r.request().url().includes("/api/charges/") ? r.fallback() : r.fulfill({ json: [] })));
  await page.route(`**/api/vehicles/${VEHICLE_ID}/lease`, (r) => r.fulfill({ status: 404, json: { error: "no lease" } }));
  await page.route("**/api/push/status", (r) => r.fulfill({ json: { enabled: pushEnabled, subscription_count: 0 } }));
  await page.route("**/api/push/vapid-key", (r) => r.fulfill({ json: { public_key: "BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U" } }));
  await page.goto("/");
}

const prompts = (page: Page) => page.evaluate(() => (window as unknown as { __prompts: number }).__prompts);

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`update alerts row · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page);
      const row = page.locator("[data-first-day-alerts]");
      await expect(row).toBeVisible();
      await expect(row).toContainText("Get a notification when a software update is offered or installs.");
      expect(await prompts(page)).toBe(0); // never on load
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container, .maplibregl-map").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `first-day-alerts-${w}-${theme}.png`) });
      await ctx.close();
    });
  }
}

test("tap asks for permission, subscribes for software updates only, then shows Alerts on", async ({ page }) => {
  await open(page);
  let body: { preferences?: Record<string, boolean> } | null = null;
  await page.route("**/api/push/subscribe", (r) => {
    body = r.request().postDataJSON();
    return r.fulfill({ json: { ok: true, preferences: body!.preferences } });
  });
  await page.getByRole("button", { name: "Turn on alerts" }).click();
  await expect(page.locator("[data-first-day-alerts]")).toContainText("Alerts on");
  expect(await prompts(page)).toBe(1);
  expect(body!.preferences).toEqual({ on_charging_done: false, on_plug_in_reminder: false, on_anomaly: false, on_software_update: true, on_milestones: false });
  await expect(page.getByRole("button", { name: "Turn on alerts" })).toHaveCount(0);
});

test("a blocked browser says so and offers no button", async ({ page }) => {
  await open(page, { permission: "denied" });
  const row = page.locator("[data-first-day-alerts]");
  await expect(row).toContainText("Notifications are blocked in this browser. You can allow them in the browser's site settings.");
  await expect(row.getByRole("button")).toHaveCount(0);
  expect(await prompts(page)).toBe(0);
});

test("declining the prompt ends in the blocked message", async ({ page }) => {
  await open(page, { prompt: "denied" });
  await page.getByRole("button", { name: "Turn on alerts" }).click();
  await expect(page.locator("[data-first-day-alerts]")).toContainText("Notifications are blocked in this browser");
});

test("Not now hides the row and it stays hidden", async ({ page }) => {
  await open(page);
  await page.getByRole("button", { name: "Not now" }).click();
  await expect(page.locator("[data-first-day-alerts]")).toHaveCount(0);
  await expect(page.locator("[data-first-day]")).toBeVisible();
  await page.reload();
  await expect(page.locator("[data-first-day]")).toBeVisible();
  await expect(page.locator("[data-first-day-alerts]")).toHaveCount(0);
});

test("no row when the server has no push configured", async ({ page }) => {
  await open(page, { pushEnabled: false });
  await expect(page.locator("[data-first-day]")).toBeVisible();
  await page.waitForTimeout(400);
  await expect(page.locator("[data-first-day-alerts]")).toHaveCount(0);
});

test("an already-subscribed device: the button waits for the check, then shows Alerts on without re-posting", async ({ page }) => {
  let posts = 0;
  await page.route("**/api/push/subscribe", (r) => {
    posts++;
    return r.fulfill({ json: { ok: true, preferences: {} } });
  });
  await open(page, { permission: "granted", existing: true, checkDelayMs: 800 });
  const row = page.locator("[data-first-day-alerts]");
  await expect(row.getByRole("button", { name: "Turn on alerts" })).toBeDisabled();
  await expect(row).toContainText("Alerts on");
  expect(posts).toBe(0);
});
