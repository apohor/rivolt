import { describe, expect, it } from "vitest";
import { termsGateActive } from "./terms";

const me = (required: boolean | undefined, accepted: string | null = null) => ({ terms: { current: "2026-10-17", accepted, accepted_at: null, ...(required === undefined ? {} : { required }) } });

describe("termsGateActive", () => {
  it("gates only when the server says the account must accept", () => {
    expect(termsGateActive(me(true), "/")).toBe(true);
    expect(termsGateActive(me(true), "/money")).toBe(true);
  });
  it("existing users never see it, whatever they accepted (or didn't)", () => {
    expect(termsGateActive(me(false, null), "/")).toBe(false);
    expect(termsGateActive(me(false, "2026-01-01"), "/")).toBe(false);
    expect(termsGateActive(me(undefined, null), "/")).toBe(false);
  });
  it("never gates signed-out visitors or servers without terms", () => {
    expect(termsGateActive(null, "/")).toBe(false);
    expect(termsGateActive({}, "/")).toBe(false);
  });
  it("keeps the legal pages, sign-in and the demo open", () => {
    for (const p of ["/privacy", "/terms", "/lease-calculator", "/login", "/auth/hydra/login", "/signup", "/signup/full", "/demo", "/demo/money"]) {
      expect(termsGateActive(me(true), p), p).toBe(false);
    }
    expect(termsGateActive(me(true), "/privacy-settings")).toBe(true);
  });
});
