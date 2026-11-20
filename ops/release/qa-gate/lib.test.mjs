import test from "node:test";
import assert from "node:assert/strict";
import { toRegExp, evaluateCapture, stateLines, tally, summariseDiff, classifyFiles, renderSheet, FAIL, PASS, NOT_SEEN } from "./lib.mjs";

const manifest = {
  viewports: [{ name: "393", width: 393, height: 852 }], schemes: ["light"],
  mustNotEverywhere: ["/\\bNaN\\b/"], consoleIgnore: ["tiles"], failedIgnore: ["^404 /lease$"],
  screens: [{ id: "now", path: "/", ready: "x", states: { charging: { must: ["/kW/"] }, driving: { must: ["/mph/"] } } }],
};
const cap = (o = {}) => ({ screen: "now", vp: "393", scheme: "light", ready: true, state: null, text: "Now", console: [], failed: [], overflow: { scrollWidth: 393, innerWidth: 393 }, shot: "a.png", ...o });
const status = (lines, check) => lines.find((l) => l.check === check)?.status;

test("toRegExp: slashes carry flags, a bare string is literal", () => {
  assert.ok(toRegExp("/kw/i").test("12 kW"));
  assert.ok(toRegExp("a.b").test("a.b"));
  assert.ok(!toRegExp("a.b").test("axb"));
});

test("a clean capture passes every machine check", () => {
  const l = evaluateCapture(manifest, manifest.screens[0], cap());
  assert.equal(tally(l)[FAIL], 0);
  assert.equal(status(l, "loaded"), PASS);
});

test("console errors, failed requests, overflow, banned text and not-ready each fail their own line", () => {
  const l = evaluateCapture(manifest, manifest.screens[0], cap({ ready: false, console: ["boom", "tiles 404"], failed: ["500 /api/x"], overflow: { scrollWidth: 500, innerWidth: 393 }, text: "NaN kW" }));
  for (const c of ["loaded", "no-console-errors", "no-failed-requests", "no-horizontal-overflow", "never-shows /\\bNaN\\b/"]) assert.equal(status(l, c), FAIL, c);
  assert.match(l.find((x) => x.check === "no-console-errors").detail, /boom/);
  assert.doesNotMatch(l.find((x) => x.check === "no-console-errors").detail, /tiles/);
});

test("an expected failed request is ignored", () => {
  assert.equal(status(evaluateCapture(manifest, manifest.screens[0], cap({ failed: ["404 /lease"] })), "no-failed-requests"), PASS);
});

test("ignored console noise does not fail", () => {
  assert.equal(status(evaluateCapture(manifest, manifest.screens[0], cap({ console: ["tiles 416"] })), "no-console-errors"), PASS);
});

test("state lines apply only to the state the slot showed; unseen states become NOT SEEN", () => {
  const seenCharging = cap({ state: "charging", text: "Charging 11 kW" });
  const l = evaluateCapture(manifest, manifest.screens[0], seenCharging);
  assert.equal(status(l, "shows /kW/"), PASS);
  assert.equal(status(l, "state charging"), PASS);
  assert.equal(l.find((x) => x.check === "shows /mph/"), undefined);
  const ns = stateLines(manifest, [seenCharging]);
  assert.deepEqual(ns.map((x) => [x.check, x.status]), [["state driving", NOT_SEEN]]);
});

test("a charging state without its text fails", () => {
  assert.equal(status(evaluateCapture(manifest, manifest.screens[0], cap({ state: "charging", text: "Charging" })), "shows /kW/"), FAIL);
});

test("diff summary lists PRs and says when only notes differ", () => {
  const d = summariseDiff("aaa", "bbb", "1111111aaaa\tFix thing (#12)\n2222222bbbb\tNotes for v1 (#13) and #14", "release-notes/v1.md\ndocs/x.md\n");
  assert.deepEqual(d.prs, [12, 13, 14]);
  assert.equal(d.carries, true);
  assert.equal(summariseDiff("a", "b", "", "web/src/a.tsx\nrelease-notes/v1.md").carries, false);
  assert.deepEqual(classifyFiles(["internal/api/x.go", "ios/A.swift", "deploy/h.yaml"]).area.server, ["internal/api/x.go"]);
});

test("the sheet escapes text and lists failures first", () => {
  const html = renderSheet({ meta: { base: "<b>", at: "t", seconds: 1 }, lines: [{ id: "x", status: FAIL, detail: "<script>" }, { id: "y", status: PASS, detail: "" }], caps: [cap()], diff: null, manifest });
  assert.match(html, /1 PASS &middot; 1 FAIL/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /a\.png/);
});
