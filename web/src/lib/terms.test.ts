import { describe, expect, it } from "vitest";
import { termsGateActive } from "./terms";

const me = (accepted: string | null, current = "2026-10-17") => ({ terms: { current, accepted, accepted_at: null } });

describe("termsGateActive", () => {
  it("gates a user who never accepted, or accepted an older version", () => {
    expect(termsGateActive(me(null), "/")).toBe(true);
    expect(termsGateActive(me("2026-01-01"), "/money")).toBe(true);
  });
  it("lets an up-to-date user through", () => {
    expect(termsGateActive(me("2026-10-17"), "/")).toBe(false);
  });
  it("never gates signed-out visitors or servers without terms", () => {
    expect(termsGateActive(null, "/")).toBe(false);
    expect(termsGateActive({}, "/")).toBe(false);
  });
  it("keeps the legal pages, sign-in and the demo open", () => {
    for (const p of ["/privacy", "/terms", "/login", "/auth/hydra/login", "/signup", "/signup/full", "/demo", "/demo/money"]) {
      expect(termsGateActive(me(null), p), p).toBe(false);
    }
    expect(termsGateActive(me(null), "/privacy-settings")).toBe(true);
  });
});
