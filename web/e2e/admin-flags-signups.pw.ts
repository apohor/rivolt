import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Admin > Signups (cap usage + queue) and Operations (kill switch with a typed confirm, flags with
// who changed them), new look.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const H = 3_600_000;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const ADMIN = { id: "u-sample", username: "sample@example.com", email: "sample@example.com", display_name: "Owner", role: "admin", disabled: false, created_at: ago(900 * H), vehicle_count: 1, drive_count: 1, import_count: 0, rivian_connected: true, needs_reauth: false };

type State = { paused: boolean; reason: string; planner: boolean; cap: number; used: number };

async function open(page: Page, tab: "signups" | "operations", init: Partial<State> = {}) {
  const st: State = { paused: false, reason: "", planner: true, cap: 25, used: 12, ...init };
  const calls: string[] = [];
  const reqs = [
    { id: "r1", email: "newbie@example.com", message: "I have an R1S", status: "pending", requested_at: ago(2 * H) },
    { id: "r2", email: "second@example.com", message: "", status: "pending", requested_at: ago(30 * H) },
    { id: "r3", email: "done@example.com", message: "", status: "approved", requested_at: ago(90 * H), signup_token: "t", token_used_at: ago(80 * H) },
  ];
  await stubApi(page, { uiNext: true });
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: [ADMIN] } }));
  await page.route("**/api/admin/audit**", (r) => {
    const action = new URL(r.request().url()).searchParams.get("action");
    const at = { "flag.kill_switch": ago(5 * H), "flag.trip_planner": ago(50 * H), "flag.signup_cap": ago(26 * H) }[action ?? ""];
    return r.fulfill({ json: { items: at ? [{ id: 1, at, actor_user_id: ADMIN.id, action, target_type: "flag", target_id: "x", details: {}, ip: "1.2.3.4", outcome: "ok", status: 200 }] : [], next_before: null } });
  });
  await page.route("**/api/admin/kill-switch", async (r) => {
    if (r.request().method() === "PUT") {
      const b = r.request().postDataJSON();
      calls.push(`kill ${JSON.stringify(b)}`);
      st.paused = b.paused; st.reason = b.reason;
    }
    return r.fulfill({ json: { kill_switch: { paused: st.paused, reason: st.reason }, trip_planner: { enabled: st.planner } } });
  });
  await page.route("**/api/admin/flags/trip-planner", (r) => {
    const b = r.request().postDataJSON();
    calls.push(`planner ${b.enabled}`);
    st.planner = b.enabled;
    return r.fulfill({ json: { trip_planner: { enabled: st.planner } } });
  });
  await page.route("**/api/admin/signup-cap", (r) => {
    if (r.request().method() === "PUT") { const b = r.request().postDataJSON(); calls.push(`cap ${b.limit}`); st.cap = b.limit; }
    return r.fulfill({ json: { signup_cap: { limit: st.cap }, used: st.used } });
  });
  await page.route("**/api/admin/signup-requests**", (r) => {
    const status = new URL(r.request().url()).searchParams.get("status");
    return r.fulfill({ json: { requests: reqs.filter((x) => !status || x.status === status) } });
  });
  await page.route("**/api/admin/signup-requests/*/approve", (r) => {
    calls.push("approve"); return r.fulfill({ json: { request: reqs[0], link: "https://rivolt.dev/signup?token=abc", email_sent: false } });
  });
  await page.route("**/api/admin/signup-requests/*/reject", (r) => { calls.push("reject"); return r.fulfill({ json: { request: reqs[1] } }); });
  await page.goto(`/admin#${tab}`);
  await expect(page.locator(tab === "signups" ? "[data-admin-signups]" : "[data-admin-flags]")).toBeVisible();
  return calls;
}

async function clean(page: Page, name: string) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
  const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
  expect(summary, summary.join("\n")).toEqual([]);
  await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: true });
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`signups · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page, "signups");
      await expect(page.locator("[data-cap-usage]")).toContainText("12 / 25");
      await expect(page.getByText("13 seats left")).toBeVisible();
      await expect(page.getByRole("progressbar", { name: /signup cap/ })).toHaveAttribute("aria-valuenow", "12");
      await expect(page.getByText("Changed by Owner · 1d ago")).toBeVisible();
      await expect(page.getByRole("button", { name: /^pending/ })).toContainText("2");
      await expect(page.locator("[data-signup-row]")).toHaveCount(2);
      await clean(page, `admin-signups-${w}-${theme}`);
      await ctx.close();
    });

    test(`operations · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page, "operations");
      await expect(page.locator("[data-kill-state]")).toHaveText("Upstream calls are flowing");
      await expect(page.getByText("Changed by Owner · 5h ago")).toBeVisible();
      await expect(page.locator("[data-flag=trip_planner]")).toContainText("On");
      await expect(page.locator("[data-flag=trip_planner]")).toContainText("Changed by Owner · 2d ago");
      await clean(page, `admin-operations-${w}-${theme}`);
      await ctx.close();
    });
  }
}

test("cap: usage state when full, and saving a new cap", async ({ page }) => {
  const calls = await open(page, "signups", { cap: 12, used: 12 });
  await expect(page.getByText("Full: new signups are blocked")).toBeVisible();
  await page.getByLabel("New signup cap").fill("30");
  await page.getByRole("button", { name: "Save cap" }).click();
  await expect.poll(() => calls).toContain("cap 30");
  await expect(page.getByText("18 seats left")).toBeVisible();
});

test("queue: approve shows the link, reject posts, filters switch", async ({ page }) => {
  const calls = await open(page, "signups");
  await page.getByRole("button", { name: "Approve newbie@example.com" }).click();
  await expect(page.getByRole("status")).toContainText("Approved newbie@example.com: email failed, copy the link below");
  await expect(page.getByText("https://rivolt.dev/signup?token=abc")).toBeVisible();
  await page.getByRole("button", { name: "Reject second@example.com" }).click();
  await expect.poll(() => calls).toEqual(["approve", "reject"]);
  await page.getByRole("button", { name: /^approved/ }).click();
  await expect(page.locator("[data-signup-row]")).toHaveCount(1);
  await expect(page.getByText("signed up")).toBeVisible();
});

test("kill switch: pausing needs the typed word, resuming is one click", async ({ page }) => {
  const calls = await open(page, "operations");
  await page.getByRole("button", { name: "Pause Rivian calls…" }).click();
  const dlg = page.locator("[data-typed-confirm]");
  const go = dlg.getByRole("button", { name: "Pause Rivian calls" });
  await expect(go).toBeDisabled();
  await dlg.getByLabel(/Type/).fill("pause");
  await expect(go).toBeDisabled(); // exact, case-sensitive
  await dlg.getByRole("button", { name: "Cancel" }).click();
  expect(calls).toEqual([]);
  await page.getByRole("button", { name: "Pause Rivian calls…" }).click();
  await dlg.getByLabel("Reason (recorded with the pause)").fill("Rivian is rate limiting us");
  await dlg.getByLabel(/Type/).fill("PAUSE");
  await go.click();
  await expect.poll(() => calls).toEqual([`kill {"paused":true,"reason":"Rivian is rate limiting us"}`]);
  await expect(page.locator("[data-kill-state]")).toHaveText("PAUSED: Rivian is rate limiting us");
  await page.getByRole("button", { name: "Resume Rivian calls" }).click();
  await expect(page.locator("[data-kill-state]")).toHaveText("Upstream calls are flowing");
  expect(calls.at(-1)).toBe(`kill {"paused":false,"reason":""}`);
});

test("flag toggle needs one confirm", async ({ page }) => {
  const calls = await open(page, "operations");
  const row = page.locator("[data-flag=trip_planner]");
  await row.getByRole("button", { name: "Turn off" }).click();
  expect(calls).toEqual([]);
  await row.getByRole("button", { name: "Cancel" }).click();
  expect(calls).toEqual([]);
  await row.getByRole("button", { name: "Turn off" }).click();
  await row.getByRole("button", { name: "Confirm turn off" }).click();
  await expect.poll(() => calls).toEqual(["planner false"]);
  await expect(row).toContainText("Off");
});
