import test from "node:test";
import assert from "node:assert/strict";
import { pairLines } from "./sim.mjs";
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
