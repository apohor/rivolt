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
  it("reports each degraded state with the right target", () => {
    expect(degradedState(undefined, true, undefined)?.tone).toBe("rose");
    expect(degradedState(ok, false, rv({ mfa_pending: true }))?.cta).toBe("Enter code");
    expect(degradedState(ok, false, rv({ needs_reauth: true }))?.cta).toBe("Reconnect");
    expect(degradedState(ok, false, rv({ authenticated: false }))?.cta).toBe("Connect");
    expect(degradedState(ok, false, rv({ mfa_pending: true }))?.to).toBe("/settings?tab=account");
  });
});
