import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// Admin > Operations flag rows: Money tab and statement email (who they are on for), and the AI
// call cap. Each change is an editor, one confirm, then the save.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const H = 3_600_000;
const ago = (ms: number) => new Date(e2eNow() - ms).toISOString();
const mk = (id: string, name: string, role = "user") => ({ id, username: `${name.toLowerCase()}@example.com`, email: `${name.toLowerCase()}@example.com`, display_name: name, role, disabled: false, created_at: ago(900 * H), vehicle_count: 1, drive_count: 1, import_count: 0, rivian_connected: true, needs_reauth: false });
const U1 = "11111111-1111-1111-1111-111111111111", U2 = "22222222-2222-2222-2222-222222222222", U3 = "33333333-3333-3333-3333-333333333333";
const USERS = [mk(U1, "Owner", "admin"), mk(U2, "Ann"), mk(U3, "Bob")];

type S = { money: { all?: boolean; users?: string[] }; stmt: { all?: boolean; users?: string[] }; cap: number };

async function open(page: Page) {
  const st: S = { money: { users: [U2, U3] }, stmt: { all: true }, cap: 0 };
  const calls: string[] = [];
  await stubApi(page, { uiNext: true });
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: USERS } }));
  await page.route("**/api/admin/audit**", (r) => {
    const action = new URL(r.request().url()).searchParams.get("action");
    const known = ["flag.money_tab", "flag.statement_email", "flag.ai_call_cap"].includes(action ?? "");
    return r.fulfill({ json: { items: known ? [{ id: 1, at: ago(3 * H), actor_user_id: U1, action, target_type: "flag", target_id: "x", details: {}, ip: "1.2.3.4", outcome: "ok", status: 200 }] : [], next_before: null } });
  });
  await page.route("**/api/admin/kill-switch", (r) =>
    r.fulfill({ json: { kill_switch: { paused: false }, trip_planner: { enabled: true }, money_tab: st.money, statement_email: st.stmt, ai_call_cap: { daily_limit: st.cap } } }),
  );
  await page.route("**/api/admin/flags/money-tab", (r) => { const b = r.request().postDataJSON(); calls.push(`money ${JSON.stringify(b)}`); st.money = b.all ? { all: true } : { users: b.users }; return r.fulfill({ json: { money_tab: st.money } }); });
  await page.route("**/api/admin/flags/statement-email", (r) => { const b = r.request().postDataJSON(); calls.push(`stmt ${JSON.stringify(b)}`); st.stmt = b.all ? { all: true } : { users: b.users }; return r.fulfill({ json: { statement_email: st.stmt } }); });
  await page.route("**/api/admin/flags/ai-call-cap", (r) => { const b = r.request().postDataJSON(); calls.push(`cap ${b.daily_limit}`); st.cap = b.daily_limit; return r.fulfill({ json: { ai_call_cap: { daily_limit: st.cap } } }); });
  await page.goto("/admin#operations");
  await expect(page.locator("[data-admin-flags]")).toBeVisible();
  return calls;
}
const row = (page: Page, f: string) => page.locator(`[data-flag=${f}]`);

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`flag rows · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page);
      await expect(row(page, "money_tab").locator("[data-flag-value]")).toHaveText("2 users");
      await expect(row(page, "statement_email").locator("[data-flag-value]")).toHaveText("Everyone");
      await expect(row(page, "ai_call_cap").locator("[data-flag-value]")).toHaveText("No cap");
      await expect(row(page, "money_tab")).toContainText("Changed by Owner · 3h ago");
      await row(page, "money_tab").getByRole("button", { name: "Change…" }).click();
      await row(page, "money_tab").getByLabel("Selected users").check();
      await expect(row(page, "money_tab").getByLabel(/Ann/)).toBeChecked();
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `admin-flag-rows-${w}-${theme}.png`), fullPage: true });
      await ctx.close();
    });
  }
}

test("Money tab: everyone needs a confirm, and Back or Cancel send nothing", async ({ page }) => {
  const calls = await open(page);
  const r = row(page, "money_tab");
  await r.getByRole("button", { name: "Change…" }).click();
  await r.getByLabel("Everyone").check();
  await r.getByRole("button", { name: "Save…" }).click();
  await expect(r.getByRole("alertdialog")).toContainText("Turn the Money tab on for everyone?");
  await r.getByRole("button", { name: "Back" }).click();
  await r.getByRole("button", { name: "Cancel" }).click();
  expect(calls).toEqual([]);
  await r.getByRole("button", { name: "Change…" }).click();
  await r.getByLabel("Everyone").check();
  await r.getByRole("button", { name: "Save…" }).click();
  await r.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls).toEqual([`money {"all":true,"users":[]}`]);
  await expect(r.locator("[data-flag-value]")).toHaveText("Everyone");
});

test("selected users: search, pick, save needs at least one", async ({ page }) => {
  const calls = await open(page);
  const r = row(page, "statement_email");
  await r.getByRole("button", { name: "Change…" }).click();
  await r.getByLabel("Selected users").check();
  await expect(r.getByRole("button", { name: "Save…" })).toBeDisabled(); // none picked yet
  await r.getByRole("searchbox").fill("bob");
  await expect(r.getByLabel(/Ann/)).toHaveCount(0);
  await r.getByLabel(/Bob/).check();
  await r.getByRole("button", { name: "Save…" }).click();
  await expect(r.getByRole("alertdialog")).toContainText("Limit the monthly statement email to 1 user (it is on for everyone now)?");
  await r.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls).toEqual([`stmt {"all":false,"users":["${U3}"]}`]);
  await expect(r.locator("[data-flag-value]")).toHaveText("1 user");
});

test("nobody turns it off after a confirm; an unchanged choice can't be saved", async ({ page }) => {
  const calls = await open(page);
  const r = row(page, "statement_email");
  await r.getByRole("button", { name: "Change…" }).click();
  await expect(r.getByRole("button", { name: "Save…" })).toBeDisabled();
  await r.getByLabel("Nobody").check();
  await r.getByRole("button", { name: "Save…" }).click();
  await expect(r.getByRole("alertdialog")).toContainText("Turn the monthly statement email off for everyone?");
  await r.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls).toEqual([`stmt {"all":false,"users":[]}`]);
  await expect(r.locator("[data-flag-value]")).toHaveText("Nobody");
});

test("AI call cap: validates, confirms, and 0 removes it", async ({ page }) => {
  const calls = await open(page);
  const r = row(page, "ai_call_cap");
  await r.getByRole("button", { name: "Change…" }).click();
  await expect(r.getByRole("button", { name: "Save…" })).toBeDisabled(); // 0 -> 0
  await r.getByLabel("Calls per user per day").fill("-3");
  await expect(r.getByRole("alert")).toContainText("Enter a whole number, 0 or more.");
  await expect(r.getByRole("button", { name: "Save…" })).toBeDisabled();
  await r.getByLabel("Calls per user per day").fill("50");
  await r.getByRole("button", { name: "Save…" }).click();
  await expect(r.getByRole("alertdialog")).toContainText("Cap AI calls at 50 per user per day?");
  await r.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls).toEqual(["cap 50"]);
  await expect(r.locator("[data-flag-value]")).toHaveText("50 calls per user per day");
  await r.getByRole("button", { name: "Change…" }).click();
  await r.getByLabel("Calls per user per day").fill("0");
  await r.getByRole("button", { name: "Save…" }).click();
  await expect(r.getByRole("alertdialog")).toContainText("Remove the AI call cap?");
  await r.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls.at(-1)).toBe("cap 0");
  await expect(r.locator("[data-flag-value]")).toHaveText("No cap");
});

test("rows the server does not report are not shown", async ({ page }) => {
  await stubApi(page, { uiNext: true });
  await page.route("**/api/admin/kill-switch", (r) => r.fulfill({ json: { kill_switch: { paused: false }, trip_planner: { enabled: true } } }));
  await page.route("**/api/admin/audit**", (r) => r.fulfill({ json: { items: [], next_before: null } }));
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: USERS } }));
  await page.goto("/admin#operations");
  await expect(page.locator("[data-flag=trip_planner]")).toBeVisible();
  await expect(page.locator("[data-flag=money_tab]")).toHaveCount(0);
  await expect(page.locator("[data-flag=ai_call_cap]")).toHaveCount(0);
});
