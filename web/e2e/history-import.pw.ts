import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Settings → Data → Import history: pick a source, add a file, check the preview, import, then undo it from the Imports list.
// The API is stubbed; the contract it follows is the one in history-import-design.md.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const PREVIEW = {
  sha256: "ab".repeat(32),
  source: "tezlab",
  first: "2024-03-02T08:00:00Z",
  last: "2025-06-20T08:00:00Z",
  rows: 40,
  new_drives: 12,
  new_charges: 4,
  skipped: { overlaps_live: 3 },
  assumptions: { soc_scale: "percent", distance_unit: "mi", energy_unit: "kwh", tz: "America/Los_Angeles" },
  warnings: ["ambiguous_local_times"],
  sample: [{ kind: "drive", started_at: "2024-03-02T08:10:00Z", ended_at: "2024-03-02T08:40:00Z", distance_mi: 14.2, energy_kwh: 5.1, start_soc_pct: 80, end_soc_pct: 74 }],
};

async function setup(page: Page) {
  await stubApi(page, { historyImport: true });
  await page.route("**/api/vehicles/owned", (r) => r.fulfill({ json: { vehicles: [{ id: "o1", rivian_vehicle_id: "veh-1", display_name: "Sample R1S", model: "R1S" }] } }));
  const state = { imports: [] as Record<string, unknown>[], undone: false, posts: [] as string[] };
  await page.route("**/api/import/preview", (r) => r.fulfill({ json: PREVIEW }));
  await page.route("**/api/import/commit", (r) => {
    const body = r.request().postData() ?? "";
    state.posts.push(body.includes(PREVIEW.sha256) ? "sha" : "nosha");
    state.imports = [{ id: "imp-1", source: "tezlab", started_at: "2025-07-01T12:00:00Z", rows: 40, drives: 12, charges: 4, first: PREVIEW.first, last: PREVIEW.last }];
    const nd = [{ type: "start", id: "imp-1", total: 16 }, { type: "progress", done: 8, total: 16 }, { type: "done", id: "imp-1", drives: 12, charges: 4, skipped: {} }].map((e) => JSON.stringify(e)).join("\n") + "\n";
    return r.fulfill({ status: 200, contentType: "application/x-ndjson", body: nd });
  });
  await page.route("**/api/imports", (r) => r.fulfill({ json: { imports: state.imports } }));
  await page.route("**/api/imports/imp-1/undo", (r) => {
    state.undone = true;
    // The server keeps the record and stamps undone_at.
    state.imports = state.imports.map((i) => ({ ...i, undone_at: "2025-07-01T12:30:00Z" }));
    return r.fulfill({ json: { drives: 12, charges: 4 } });
  });
  return state;
}

for (const w of [390, 1280] as const) {
  test(`import → undo · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    const state = await setup(page);
    await page.goto("/settings?tab=data#import-history");
    const card = page.locator("[data-history-import]");
    await card.getByLabel("TezLab").check();
    await card.getByRole("button", { name: "Continue" }).click();
    await expect(card.getByText("Export from TezLab")).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS, `import-files-${w}.png`) });
    await card.locator('input[type="file"]').setInputFiles([
      { name: "drives.csv", mimeType: "text/csv", buffer: Buffer.from("a,b\n1,2\n") },
      { name: "charges.csv", mimeType: "text/csv", buffer: Buffer.from("a,b\n1,2\n") },
    ]);
    await card.getByRole("button", { name: "Check file" }).click();
    await expect(card.getByText("12 drives and 4 charges will be added")).toBeVisible();
    await expect(card.getByText("3 skipped: already recorded by your vehicle")).toBeVisible();
    await expect(card.getByText(/clocks change/)).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS, `import-preview-${w}.png`), fullPage: true });
    await card.getByRole("button", { name: "Import", exact: true }).click();
    await expect(card.getByText("Added 12 drives, 4 charges.")).toBeVisible();
    expect(state.posts).toEqual(["sha"]);
    const list = page.locator("[data-imports-list]");
    await expect(list.getByText("12 drives, 4 charges")).toBeVisible();
    await list.getByRole("button", { name: /^Undo TezLab import/ }).click();
    await expect(list.getByText("This removes 12 drives and 4 charges. Notes or tags you added to them go too.")).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS, `import-undo-${w}.png`), fullPage: true });
    await list.getByRole("button", { name: "Remove them" }).click();
    await expect(list.getByText(/^Undone /)).toBeVisible();
    expect(state.undone).toBe(true);
    // The wizard's banner follows the record: no stale "Added" after the undo.
    await expect(card.getByText("Removed 12 drives, 4 charges.")).toBeVisible();
    await expect(card.getByText("Added 12 drives, 4 charges.")).toHaveCount(0);
    await expect(card.getByRole("link", { name: "See drives" })).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
    const ax = await new AxeBuilder({ page }).include("#import-history").include("#imports").withTags(["wcag2a", "wcag2aa"]).analyze();
    const sum = ax.violations.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(" ")).join(" | ")}`);
    expect(sum, sum.join("\n")).toEqual([]);
    await ctx.close();
  });
}

test("unrecognized columns show header names and a mail link", async ({ page }) => {
  await setup(page);
  await page.route("**/api/import/preview", (r) => r.fulfill({ status: 422, json: { error: "no", code: "unknown_columns", header: ["foo", "bar"] } }));
  await page.goto("/settings?tab=data#import-history");
  const card = page.locator("[data-history-import]");
  await card.getByRole("button", { name: "Continue" }).click();
  await card.locator('input[type="file"]').setInputFiles({ name: "x.csv", mimeType: "text/csv", buffer: Buffer.from("foo,bar\n") });
  await card.getByRole("button", { name: "Check file" }).click();
  await expect(card.getByText("We don't recognize this file's columns.")).toBeVisible();
  await expect(card.getByText("foo, bar")).toBeVisible();
  await expect(card.getByRole("link", { name: "Send us this header line" })).toBeVisible();
});

test("flag off hides the wizard and keeps the ElectraFi card", async ({ page }) => {
  await stubApi(page);
  await page.goto("/settings?tab=data");
  await expect(page.getByText("Import ElectraFi CSV")).toBeVisible();
  await expect(page.locator("[data-history-import]")).toHaveCount(0);
});

test("a file that needs a choice asks for it, then previews", async ({ page }) => {
  await setup(page);
  let n = 0;
  await page.route("**/api/import/preview", (r) => {
    n++;
    const sent = r.request().postData() ?? "";
    if (!sent.includes('name="distance_unit"')) return r.fulfill({ status: 422, json: { error: "no", code: "need_choice", need: ["distance_unit"] } });
    return r.fulfill({ json: PREVIEW });
  });
  await page.goto("/settings?tab=data#import-history");
  const card = page.locator("[data-history-import]");
  await card.getByRole("button", { name: "Continue" }).click();
  await card.locator('input[type="file"]').setInputFiles({ name: "x.csv", mimeType: "text/csv", buffer: Buffer.from("a\n") });
  await card.getByRole("button", { name: "Check file" }).click();
  await card.getByLabel("Distances in").selectOption("mi");
  await card.getByRole("button", { name: "Check file" }).click();
  await expect(card.getByText("12 drives and 4 charges will be added")).toBeVisible();
  expect(n).toBe(2);
});

test("undoing an already-imported file asks first", async ({ page }) => {
  const state = await setup(page);
  state.imports = [{ id: "imp-1", source: "tezlab", started_at: "2025-07-01T12:00:00Z", rows: 40, drives: 12, charges: 4, first: PREVIEW.first, last: PREVIEW.last }];
  await page.route("**/api/import/preview", (r) => r.fulfill({ json: { ...PREVIEW, already_imported: { id: "imp-1", at: "2025-07-01T12:00:00Z" } } }));
  await page.goto("/settings?tab=data#import-history");
  const card = page.locator("[data-history-import]");
  await card.getByRole("button", { name: "Continue" }).click();
  await card.locator('input[type="file"]').setInputFiles({ name: "x.csv", mimeType: "text/csv", buffer: Buffer.from("a\n") });
  await card.getByRole("button", { name: "Check file" }).click();
  await card.getByRole("button", { name: "Undo that import" }).click();
  await expect(card.getByText("This removes 12 drives and 4 charges.")).toBeVisible();
  expect(state.undone).toBe(false);
  await card.getByRole("button", { name: "Remove them" }).click();
  await expect.poll(() => state.undone).toBe(true);
});

test("a commit refused because another import is running says so and can be retried", async ({ page }) => {
  await setup(page);
  let n = 0;
  await page.route("**/api/import/commit", (r) => {
    n++;
    if (n === 1) return r.fulfill({ status: 429, headers: { "Retry-After": "10" }, json: { error: "busy", code: "in_progress" } });
    return r.fulfill({ status: 200, contentType: "application/x-ndjson", body: JSON.stringify({ type: "done", id: "imp-1", drives: 12, charges: 4, skipped: {} }) + "\n" });
  });
  await page.goto("/settings?tab=data#import-history");
  const card = page.locator("[data-history-import]");
  await card.getByRole("button", { name: "Continue" }).click();
  await card.locator('input[type="file"]').setInputFiles({ name: "x.csv", mimeType: "text/csv", buffer: Buffer.from("a\n") });
  await card.getByRole("button", { name: "Check file" }).click();
  await card.getByRole("button", { name: "Import", exact: true }).click();
  await expect(card.getByText(/Another import of yours is running/)).toBeVisible();
  await card.getByRole("button", { name: "Import", exact: true }).click();
  await expect(card.getByText("Added 12 drives, 4 charges.")).toBeVisible();
});

async function toPreview(page: Page) {
  await page.goto("/settings?tab=data#import-history");
  const card = page.locator("[data-history-import]");
  await card.getByRole("button", { name: "Continue" }).click();
  await card.locator('input[type="file"]').setInputFiles({ name: "x.csv", mimeType: "text/csv", buffer: Buffer.from("a\n") });
  await card.getByRole("button", { name: "Check file" }).click();
  return card;
}

test("a gateway timeout on commit does not claim a plain failure", async ({ page }) => {
  await setup(page);
  await page.route("**/api/import/commit", (r) => r.fulfill({ status: 524, contentType: "text/html", body: "<html>timeout</html>" }));
  const card = await toPreview(page);
  await card.getByRole("button", { name: "Import", exact: true }).click();
  await expect(card.getByText(/cannot tell whether the import went through/)).toBeVisible();
  await expect(card.getByText("Something went wrong")).toHaveCount(0);
});

test("a commit stream that ends after its first line keeps the import id and checks Imports", async ({ page }) => {
  const state = await setup(page);
  state.imports = [{ id: "imp-1", source: "tezlab", started_at: "2025-07-01T12:00:00Z", rows: 40, drives: 7, charges: 2, first: PREVIEW.first, last: PREVIEW.last }];
  await page.route("**/api/import/commit", (r) =>
    r.fulfill({ status: 200, contentType: "application/x-ndjson", body: JSON.stringify({ type: "start", id: "imp-1" }) + "\n" }),
  );
  const card = await toPreview(page);
  await card.getByRole("button", { name: "Import", exact: true }).click();
  await expect(card.getByText(/7 drives and 2 charges were saved so far/)).toBeVisible();
});

test("a connection that drops on commit is the stopped path", async ({ page }) => {
  await setup(page);
  await page.route("**/api/import/commit", (r) => r.abort("connectionreset"));
  const card = await toPreview(page);
  await card.getByRole("button", { name: "Import", exact: true }).click();
  await expect(card.getByText(/cannot tell whether the import went through/)).toBeVisible();
});

test("a failed undo says so inside the confirmation", async ({ page }) => {
  const state = await setup(page);
  state.imports = [{ id: "imp-1", source: "tezlab", started_at: "2025-07-01T12:00:00Z", rows: 40, drives: 12, charges: 4, first: PREVIEW.first, last: PREVIEW.last }];
  await page.route("**/api/import/preview", (r) => r.fulfill({ json: { ...PREVIEW, already_imported: { id: "imp-1", at: "2025-07-01T12:00:00Z" } } }));
  await page.route("**/api/imports/imp-1/undo", (r) => r.fulfill({ status: 500, json: { error: "boom" } }));
  const card = await toPreview(page);
  await card.getByRole("button", { name: "Undo that import" }).click();
  await card.getByRole("button", { name: "Remove them" }).click();
  await expect(card.getByRole("alertdialog").getByRole("alert")).toContainText("Something went wrong");
});
