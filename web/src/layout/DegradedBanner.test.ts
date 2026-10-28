import { describe, expect, it } from "vitest";
import { degradedState, expiryBanner } from "./DegradedBanner";

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

describe("the Rivian sign-in expiry notice", () => {
  it("is silent until two weeks ahead, and when the field is absent", () => {
    expect(expiryBanner(undefined)).toBeNull();
    expect(expiryBanner(15)).toBeNull();
    expect(degradedState(ok, false, rv())).toBeNull();
    expect(degradedState(ok, false, rv({ session_days_left: 90 }))).toBeNull();
  });
  it("is amber from 14 days and rose from 7, with the server's words and the way to renew", () => {
    const d14 = expiryBanner(14);
    expect(d14?.tone).toBe("amber");
    expect(d14?.text).toBe("Your Rivian sign-in ends in 14 days. Sign in again in Settings, Rivian account, to keep recording.");
    expect(d14?.cta).toBe("Sign in again");
    expect(d14?.to).toBe("/settings?tab=account");
    expect(expiryBanner(8)?.tone).toBe("amber");
    expect(expiryBanner(7)?.tone).toBe("rose");
    expect(expiryBanner(2)?.tone).toBe("rose");
  });
  it("says one day and today in their own words", () => {
    expect(expiryBanner(1)?.text).toContain("ends in 1 day.");
    expect(expiryBanner(0)?.text).toContain("ends today.");
  });
  it("yields to every worse state", () => {
    expect(degradedState(ok, false, rv({ session_days_left: 3, needs_reauth: true }))?.cta).toBe("Reconnect");
    expect(degradedState(ok, false, rv({ session_days_left: 3, mfa_pending: true }))?.cta).toBe("Enter code");
    expect(degradedState(ok, false, rv({ session_days_left: 3, paused: true }))?.text).toMatch(/paused/);
    expect(degradedState(ok, false, rv({ session_days_left: 3 }))?.tone).toBe("rose");
  });
});
