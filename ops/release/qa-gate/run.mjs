#!/usr/bin/env node
// The release QA gate: signs in once, captures the manifest's screens at every viewport x scheme in parallel browser contexts, runs the
// cheap machine checks, and writes <out>/index.html (contact sheet) and <out>/results.json (PASS / FAIL / NOT SEEN per line).
//
//   node ops/release/qa-gate/run.mjs --base https://preview.rivolt.dev --out /tmp/gate [--expect-version 1.2.3-rc.1 --wait-version 40]
//        [--validated <sha|tag> --rc <sha|tag>] [--browser webkit|chromium] [--no-signout] [--manifest file.json]
//
// Credentials: RIVOLT_QA_USER and RIVOLT_QA_PASS, else the macOS Keychain item named by --keychain (default rivolt-preview-login).
// They are never printed or written. Playwright comes from web/node_modules (the repo's pinned version): run `npm ci` in web/ first.
import { createRequire } from "node:module";
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { pairLines } from "./sim.mjs";
import { evaluateCapture, stateLines, tally, summariseDiff, diffText, renderSheet, ignorable, toRegExp, FAIL, PASS } from "./lib.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, "../../..");
const { values: a } = parseArgs({
  options: {
    base: { type: "string", default: "https://preview.rivolt.dev" },
    out: { type: "string" },
    manifest: { type: "string", default: join(here, "manifest.json") },
    browser: { type: "string", default: "webkit" },
    "expect-version": { type: "string" },
    "wait-version": { type: "string", default: "0" },
    validated: { type: "string" },
    rc: { type: "string" },
    web: { type: "string", default: join(repo, "web") },
    keychain: { type: "string", default: "rivolt-preview-login" },
    "no-signout": { type: "boolean", default: false },
    reads: { type: "string", default: "8" },
    sim: { type: "boolean", default: false },
    "admin-base": { type: "string" },
    "access-keychain": { type: "string", default: "rivolt.qa-access-preview" },
  },
});
if (!a.out) { console.error("usage: run.mjs --base URL --out DIR [options]; see the header of this file"); process.exit(2); }
const base = a.base.replace(/\/$/, "");
const out = resolve(a.out);
mkdirSync(out, { recursive: true });
const manifest = JSON.parse(readFileSync(a.manifest, "utf8"));
const t0 = Date.now();

let pw;
try { pw = createRequire(join(a.web, "package.json"))("@playwright/test"); } catch { console.error(`Playwright not found under ${a.web}/node_modules: run "npm ci" in web/ (or pass --web)`); process.exit(2); }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function credentials() {
  if (process.env.RIVOLT_QA_USER && process.env.RIVOLT_QA_PASS) return { user: process.env.RIVOLT_QA_USER, pass: process.env.RIVOLT_QA_PASS };
  const sec = (args) => execFileSync("security", args, { encoding: "utf8" });
  const user = /"acct"<blob>="([^"]*)"/.exec(sec(["find-generic-password", "-s", a.keychain]))?.[1];
  const pass = sec(["find-generic-password", "-s", a.keychain, "-w"]).trim();
  if (!user || !pass) throw new Error(`no credentials in the Keychain item ${a.keychain}`);
  return { user, pass };
}

async function health() {
  try { return (await (await fetch(`${base}/api/health`, { signal: AbortSignal.timeout(15000) })).json()).version ?? null; } catch { return null; }
}

// N quick /api/health reads (N at least twice the replicas, so every pod answers): the versions seen, null for a read that failed.
async function healthSample() {
  const got = [];
  for (let i = 0; i < Number(a.reads); i++) { got.push(await health()); await sleep(300); }
  return got;
}

// Waits for the expected version on three reads in a row (a half-rolled preview can still answer from an old pod).
async function versionGate() {
  const want = a["expect-version"]?.replace(/^v/, "");
  const deadline = Date.now() + Number(a["wait-version"]) * 60000;
  let got = await health(), seen = 0;
  for (;;) {
    seen = want && got === want ? seen + 1 : 0;
    if (!want || seen >= 3 || Date.now() > deadline) break;
    await sleep(20000);
    got = await health();
  }
  return { version: got, want };
}

async function signIn(browser, root = base, headers = {}) {
  const { user, pass } = credentials();
  const ctx = await browser.newContext({ extraHTTPHeaders: headers });
  const pg = await ctx.newPage();
  await pg.goto(`${root}/login`, { waitUntil: "domcontentloaded" });
  const email = pg.locator("input[autocomplete=username]");
  const choose = pg.getByRole("button", { name: /Continue with Email/ });
  await email.or(choose).first().waitFor({ timeout: 30000 });
  if (!(await email.isVisible())) await choose.click();
  await email.fill(user);
  await pg.locator("input[autocomplete=current-password]").fill(pass);
  await pg.locator("button[type=submit]").click();
  await pg.waitForURL((u) => !/login|auth/.test(u.pathname), { timeout: 30000 });
  const state = await ctx.storageState();
  await ctx.close();
  return state;
}

// Books the short sim pair on the preview admin site (behind Cloudflare Access: a service token from the Keychain, never printed) and returns
// the check lines. The drive plays about 6 min, the charge queues behind it; the screens run while they play.
async function bookSim(browser, stage) {
  const adminBase = (a["admin-base"] ?? base.replace("//preview.", "//admin-preview.")).replace(/\/$/, "");
  const sec = (args) => execFileSync("security", args, { encoding: "utf8" });
  const id = /"acct"<blob>="([^"]*)"/.exec(sec(["find-generic-password", "-s", a["access-keychain"]]))?.[1];
  const headers = { "CF-Access-Client-Id": id, "CF-Access-Client-Secret": sec(["find-generic-password", "-s", a["access-keychain"], "-w"]).trim() };
  const state = await signIn(browser, adminBase, headers);
  const ctx = await browser.newContext({ storageState: state, extraHTTPHeaders: headers });
  const post = async (path) => { const r = await ctx.request.post(`${adminBase}/api/admin/sim/${path}`, { data: {} }); return { status: r.status(), body: await r.json().catch(() => ({})) }; };
  const first = await post("drive?short=1"); stage("sim drive booked");
  const second = first.status === 200 ? await post("charge?short=1&queue=1") : { status: 0, body: {} }; stage("sim charge booked");
  await ctx.close();
  return pairLines(first, second);
}

function watch(pg, bag) {
  pg.on("console", (m) => { if (m.type() === "error") bag.console.push(m.text().slice(0, 160)); });
  pg.on("pageerror", (e) => bag.console.push(`pageerror: ${e.message.slice(0, 160)}`));
  pg.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/") && !u.pathname.startsWith("/api/maps/") && r.status() >= 400) bag.failed.push(`${r.status()} ${u.pathname}`);
  });
  pg.on("requestfailed", (r) => {
    const u = new URL(r.url());
    if (u.origin === base && !/aborted|cancel/i.test(r.failure()?.errorText ?? "") && !ignorable(r.url(), manifest.consoleIgnore ?? [])) bag.failed.push(`failed ${u.pathname}: ${r.failure()?.errorText}`);
  });
}

async function capture(ctx, screen, vp, scheme) {
  const bag = { console: [], failed: [] };
  const pg = await ctx.newPage();
  watch(pg, bag);
  const cap = { screen: screen.id, vp: vp.name, scheme, ready: false, state: null, text: "", console: bag.console, failed: bag.failed, overflow: null, shot: null };
  try {
    await pg.goto(base + screen.path, { waitUntil: "domcontentloaded" });
    await pg.locator(screen.ready).first().waitFor({ state: "visible", timeout: 25000 });
    cap.ready = true;
    await pg.waitForLoadState("networkidle", { timeout: 8000 }).catch(() => {}); // map tiles may keep the network busy; the cap is the limit
    await pg.evaluate(() => document.fonts.ready);
    if (screen.stateSelector) {
      await pg.locator(screen.stateSelector).first().waitFor({ state: "attached", timeout: 3000 }).catch(() => {});
      cap.state = await pg.locator(screen.stateSelector).first().getAttribute("data-kind").catch(() => null);
    }
    cap.text = await pg.evaluate(() => document.body.innerText);
    cap.overflow = await pg.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, innerWidth: window.innerWidth }));
    cap.shot = `${screen.id}-${vp.name}-${scheme}.png`;
    await pg.screenshot({ path: join(out, cap.shot), fullPage: true });
  } catch (e) {
    cap.console.push(`gate: ${String(e.message).split("\n")[0].slice(0, 160)}`);
  }
  await pg.close();
  return cap;
}

async function captureContext(browser, state, vp, scheme) {
  const ctx = await browser.newContext({ viewport: { width: vp.width, height: vp.height }, deviceScaleFactor: vp.width < 600 ? 2 : 1, colorScheme: scheme, storageState: state });
  await ctx.addInitScript((t) => { try { localStorage.setItem("rivolt.theme", t); } catch {} }, scheme);
  const caps = [];
  for (const s of manifest.screens) caps.push(await capture(ctx, s, vp, scheme));
  await ctx.close();
  return caps;
}

// Last, because it ends the shared session.
async function signOut(browser, state) {
  const so = manifest.signOut, lines = [];
  const mk = (check, status, detail = "") => ({ id: `signout/393/light/${check}`, screen: "signout", vp: "393", scheme: "light", check, status, detail });
  const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: "light", storageState: state });
  const pg = await ctx.newPage();
  try {
    await pg.goto(base + so.path, { waitUntil: "domcontentloaded" });
    await pg.getByRole("button", { name: so.button }).first().click({ timeout: 25000 });
    await pg.waitForURL((u) => !u.pathname.startsWith(so.afterMustNot), { timeout: 20000 });
    await pg.waitForLoadState("networkidle", { timeout: 8000 }).catch(() => {});
    const text = await pg.evaluate(() => document.body.innerText);
    lines.push(mk("leaves-settings", PASS, pg.url().replace(base, "")));
    lines.push(mk("shows-sign-in", toRegExp(so.afterMust).test(text) ? PASS : FAIL));
    await pg.screenshot({ path: join(out, "signout-393-light.png"), fullPage: true });
  } catch (e) {
    lines.push(mk("sign-out", FAIL, String(e.message).split("\n")[0].slice(0, 160)));
  }
  await ctx.close();
  return lines;
}

function diffSummary() {
  if (!a.validated || !a.rc) return null;
  const git = (...args) => execFileSync("git", ["-C", repo, ...args], { encoding: "utf8" });
  try { git("cat-file", "-e", `${a.validated}^{commit}`); git("cat-file", "-e", `${a.rc}^{commit}`); } catch { git("fetch", "--quiet", "origin", a.validated, a.rc); }
  const range = `${a.validated}..${a.rc}`;
  return summariseDiff(a.validated, a.rc, git("log", "--format=%H%x09%s", range), git("diff", "--name-only", `${a.validated}...${a.rc}`));
}

// One line per stage, `STAGE <iso time> <name>`, also kept in results.json: the gate's own duration is measured, and a delivery-metrics
// reader can take the lines as events. The sim stages (sim drive/charge start/end) use the same call when the runner books them.
const stages = [];
const stage = (name) => { const at = new Date().toISOString(); stages.push({ name, at }); console.log(`STAGE ${at} ${name}`); };
stage("start");
const vg = await versionGate();
const before = await healthSample();
let simLines = [];
const steady = new Set(before).size === 1 && before[0] && (!vg.want || before[0] === vg.want);
if (a.sim && !steady) simLines = [{ id: "sim:book", screen: "sim", vp: "-", scheme: "-", check: "book", status: FAIL, detail: `not booked: preview is not steady on one version (${[...new Set(before)].join(", ")})` }];
else if (a.sim) { try { const b0 = await pw[a.browser].launch(); simLines = await bookSim(b0, stage); await b0.close(); } catch (e) { simLines = [{ id: "sim:book", screen: "sim", vp: "-", scheme: "-", check: "book", status: FAIL, detail: String(e.message).split("\n")[0] }]; } }
stage("version");
const browser = await pw[a.browser].launch();
let state;
try { state = await signIn(browser); } catch (e) { console.error(`sign-in failed: ${String(e.message).split("\n")[0]}`); await browser.close(); process.exit(2); }
stage("sign-in");
const combos = manifest.viewports.flatMap((vp) => manifest.schemes.map((s) => [vp, s]));
const caps = (await Promise.all(combos.map(([vp, s]) => captureContext(browser, state, vp, s)))).flat();
stage("screens done");
const after = await healthSample();
// A version change or a mix of versions during the screens (a roll under way) makes the pictures a mix of two builds: INVALID, not FAIL.
const seen = [...new Set([...before, ...after])];
const invalid = seen.length > 1 || seen.includes(null) ? `versions seen during the run: ${seen.map((v) => v ?? "no answer").join(", ")}` : null;
const lines = [];
lines.push({ id: "version", screen: "version", vp: "-", scheme: "-", check: "version", status: !vg.version ? FAIL : vg.want && vg.version !== vg.want ? FAIL : PASS, detail: `${vg.version ?? "unreachable"}${vg.want ? ` (expected ${vg.want})` : " (no --expect-version)"}` });
for (const c of caps) lines.push(...evaluateCapture(manifest, manifest.screens.find((s) => s.id === c.screen), c));
lines.push(...stateLines(manifest, caps), ...simLines);
if (!a["no-signout"] && manifest.signOut) lines.push(...(await signOut(browser, state)));
await browser.close();
stage("sign-out");

const diff = diffSummary();
stage("verdict");
const meta = { stages, invalid, base, version: vg.version, expectVersion: vg.want, at: new Date().toISOString(), seconds: Math.round((Date.now() - t0) / 1000) };
writeFileSync(join(out, "results.json"), JSON.stringify({ meta, tally: tally(lines), lines, diff }, null, 1));
writeFileSync(join(out, "index.html"), renderSheet({ meta, lines, caps, diff, manifest }));
const t = tally(lines);
for (const l of lines.filter((x) => x.status !== PASS)) console.log(`${l.status.padEnd(8)} ${l.id}${l.detail ? "  " + l.detail : ""}`);
if (diff) console.log(diffText(diff));
console.log(`${t.PASS} PASS, ${t.FAIL} FAIL, ${t["NOT SEEN"]} NOT SEEN in ${meta.seconds}s -> ${join(out, "index.html")}`);
if (invalid) { console.log(`INVALID  ${invalid}: the gate starts only when preview is stable on the rc; run it again`); process.exit(3); }
process.exit(t.FAIL ? 1 : 0);
