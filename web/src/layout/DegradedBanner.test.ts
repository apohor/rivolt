import { describe, expect, it } from "vitest";
import { degradedState } from "./DegradedBanner";

const ok = { ok: true };
const rv = (o: object = {}) => ({ enabled: true, authenticated: true, mfa_pending: false, ...o });

describe("degradedState", () => {
  it("is silent when healthy, loading, or on a stub build", () => {
    expect(degradedState(ok, false, rv())).toBeNull();
    expect(degradedState(undefined, false, undefined)).toBeNull();
    expect(degradedState(ok, false, undefined)).toBeNull();
    expect(degradedState(ok, false, rv({ enabled: false, authenticated: false }))).toBeNull();
  });
  it("reports a paused connection ahead of every per-user state, with no action", () => {
    const d = degradedState(ok, false, rv({ paused: true, authenticated: false, mfa_pending: true, needs_reauth: true }));
    expect(d?.text).toMatch(/paused its connection to Rivian/);
    expect(d?.tone).toBe("amber");
    expect(d?.to).toBeUndefined();
    // Healthy and not paused stays silent.
    expect(degradedState(ok, false, rv({ paused: false }))).toBeNull();
    // A down backend still wins (we can't even say what's paused).
    expect(degradedState(undefined, true, rv({ paused: true }))?.tone).toBe("rose");
  });
  it("reports each degraded state with the right target", () => {
    expect(degradedState(undefined, true, undefined)?.tone).toBe("rose");
    expect(degradedState(ok, false, rv({ mfa_pending: true }))?.cta).toBe("Enter code");
    expect(degradedState(ok, false, rv({ needs_reauth: true }))?.cta).toBe("Reconnect");
    expect(degradedState(ok, false, rv({ authenticated: false }))?.cta).toBe("Connect");
    expect(degradedState(ok, false, rv({ mfa_pending: true }))?.to).toBe("/settings?tab=account");
  });
});

describe("the Rivian connection expiry banner (N49)", () => {
  const soon = (days: number, at = "2026-11-03T18:00:00Z") => rv({ session_days_left: days, session_expires_at: at });
  it("is silent above 7 days and when the fields are absent", () => {
    expect(degradedState(ok, false, rv())).toBeNull();
    expect(degradedState(ok, false, soon(8))).toBeNull();
    expect(degradedState(ok, false, rv({ session_days_left: 90 }))).toBeNull();
  });
  it("from 7 days says when it expires and offers Reconnect, in one tone", () => {
    for (const d of [7, 3, 1, 0]) {
      const x = degradedState(ok, false, soon(d));
      expect(x?.tone).toBe("amber");
      expect(x?.text).toMatch(/^Your Rivian connection expires \w{3} \d{1,2}\.$/);
      expect(x?.cta).toBe("Reconnect");
      expect(x?.to).toBe("/settings?tab=account");
    }
  });
  it("yields to every worse state", () => {
    expect(degradedState(ok, false, rv({ session_days_left: 3, needs_reauth: true }))?.text).toMatch(/expired/);
    expect(degradedState(ok, false, rv({ session_days_left: 3, mfa_pending: true }))?.cta).toBe("Enter code");
    expect(degradedState(ok, false, rv({ session_days_left: 3, paused: true }))?.text).toMatch(/paused/);
  });
});
