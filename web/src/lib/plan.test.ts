import { describe, expect, it } from "vitest";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import trial from "../../../ios/RivoltTests/Fixtures/me_plan_trial.json";
import stripe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";
import apple from "../../../ios/RivoltTests/Fixtures/me_plan_apple.json";
import founding from "../../../ios/RivoltTests/Fixtures/me_plan_founding.json";
import lapsed from "../../../ios/RivoltTests/Fixtures/me_plan_lapsed.json";
import billingOff from "../../../ios/RivoltTests/Fixtures/me.json";
import { daysLeft, planDate, planState } from "./plan";

// The server's own fixtures (backend-2's #1367), so a drift in the plan fields fails the build.
describe("planState from the server's fixtures", () => {
  it("is off wherever the server does not bill: billing off, the demo, an absent plan, nobody", () => {
    expect(planState(billingOff)).toEqual({ kind: "off" });
    expect(planState({ ...apple, plan_source: "demo" })).toEqual({ kind: "off" });
    expect(planState({ ...apple, billing_enabled: false })).toEqual({ kind: "off" });
    expect(planState({})).toEqual({ kind: "off" });
    expect(planState(null)).toEqual({ kind: "off" });
  });

  it("tells free, a trial, each paid source and a founding year apart", () => {
    expect(planState(free)).toEqual({ kind: "free" });
    expect(planState(trial)).toEqual({ kind: "trial", ends: "2026-11-14T12:00:00Z" });
    expect(planState(stripe)).toEqual({ kind: "pro", source: "stripe", periodEnd: "2026-12-01T00:00:00Z" });
    expect(planState(apple)).toEqual({ kind: "pro", source: "apple", periodEnd: "2026-12-01T00:00:00Z" });
    expect(planState(founding)).toEqual({ kind: "pro", source: "founding", periodEnd: "2027-11-01T00:00:00Z" });
    expect(planState({ ...stripe, plan_source: "something-new" })).toMatchObject({ kind: "pro", source: "grant" });
  });

  it("takes a lapse from plan_ended_at, not from a date in the past", () => {
    expect(planState(lapsed)).toEqual({ kind: "lapsed", endedOn: "2026-10-01T00:00:00Z", endedSource: "stripe" });
    expect(planState({ ...free, plan_period_end: "2020-01-01T00:00:00Z" })).toEqual({ kind: "free" });
  });

  it("never shows a malformed date", () => {
    expect(planDate("nonsense")).toBe("");
    expect(planDate(null)).toBe("");
    expect(planDate("2026-12-20T12:00:00Z")).toContain("2026");
    const now = new Date("2026-12-10T12:00:00Z");
    expect(daysLeft("2026-12-13T12:00:00Z", now)).toBe(3);
    expect(daysLeft("2026-12-01T00:00:00Z", now)).toBe(0);
    expect(daysLeft("x", now)).toBeNull();
  });
});
