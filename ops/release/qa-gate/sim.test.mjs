import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { accessRoute, pairLines } from "./sim.mjs";
import { FAIL, PASS } from "./lib.mjs";

const ok = (kind, start, end) => ({ status: 200, body: { active: { id: kind, kind, start, end } } });
const status = (ls, id) => ls.find((l) => l.id === `sim:${id}`).status;

test("a short drive with a charge queued at its end is all PASS", () => {
  const ls = pairLines(ok("drive_short", "2026-11-20T10:00:15Z", "2026-11-20T10:06:00Z"), ok("charge_short", "2026-11-20T10:06:00Z", "2026-11-20T10:11:00Z"));
  assert.ok(ls.every((l) => l.status === PASS), JSON.stringify(ls));
});

test("a full-length kind, a gap, a refusal or a long pair is a FAIL", () => {
  const d = ok("drive_short", "2026-11-20T10:00:15Z", "2026-11-20T10:06:00Z");
  assert.equal(status(pairLines(ok("drive", "2026-11-20T10:00:15Z", "2026-11-20T10:28:00Z"), ok("charge_short", "2026-11-20T10:28:00Z", "2026-11-20T10:33:00Z")), "drive kind"), FAIL);
  assert.equal(status(pairLines(d, ok("charge_short", "2026-11-20T10:07:00Z", "2026-11-20T10:12:00Z")), "charge starts when the drive ends"), FAIL);
  assert.equal(status(pairLines(d, { status: 409, body: { error: "busy" } }), "charge booked"), FAIL);
  assert.equal(status(pairLines(d, ok("charge_short", "2026-11-20T10:06:00Z", "2026-11-20T10:12:00Z")), "pair within 11 minutes"), FAIL);
});

const TOKEN = { "CF-Access-Client-Id": "id", "CF-Access-Client-Secret": "secret" };
const ADMIN = "https://admin.example";

// A fake Playwright route: what it was told to do with the request is recorded.
function fakeRoute(url, { nav = false, status = 200, location } = {}) {
  const seen = { continued: null, fetched: null, fulfilled: null };
  const request = { url: () => url, headers: () => ({ accept: "*/*" }), isNavigationRequest: () => nav };
  const route = {
    request: () => request,
    continue: async (o) => { seen.continued = o; },
    fetch: async (o) => { seen.fetched = o; return { status: () => status, headers: () => (location ? { location } : {}) }; },
    fulfill: async (o) => { seen.fulfilled = o; },
  };
  return { route, seen };
}
const hasToken = (h) => !!h && Object.keys(h).some((k) => /^cf-access/i.test(k));

test("the Access token goes to the admin origin and to no other origin", async () => {
  const h = accessRoute(ADMIN, TOKEN);
  const own = fakeRoute(`${ADMIN}/api/admin/sim`); await h(own.route);
  assert.ok(hasToken(own.seen.continued.headers));
  for (const url of ["https://identity.example/login", "https://fonts.example/a.woff2", "https://rivian.example/pic.webp"]) {
    const other = fakeRoute(url); await h(other.route);
    assert.ok(!hasToken(other.seen.continued.headers), `${url} must not carry the token`);
    const nav = fakeRoute(url, { nav: true }); await h(nav.route);
    assert.ok(!hasToken(nav.seen.fetched.headers), `${url} (navigation) must not carry the token`);
  }
});

test("a redirect leg is followed by hand and the next navigation is routed", async () => {
  const h = accessRoute(ADMIN, TOKEN);
  const r = fakeRoute("https://identity.example/cb", { nav: true, status: 302, location: `${ADMIN}/api/auth/oidc/callback?code=1` });
  await h(r.route);
  assert.equal(r.seen.fulfilled.status, 200);
  assert.match(r.seen.fulfilled.body, /refresh" content="0;url=https:\/\/admin\.example\/api\/auth\/oidc\/callback\?code=1"/);
  assert.equal(r.seen.fetched.maxRedirects, 0);
});

test("no browser context in the runner carries the token as a context-wide header", () => {
  for (const f of ["run.mjs", "sim.mjs"]) assert.doesNotMatch(readFileSync(new URL(f, import.meta.url), "utf8"), /extraHTTPHeaders\s*:/, f);
});
