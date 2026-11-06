import { describe, expect, it } from "vitest";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import trial from "../../../ios/RivoltTests/Fixtures/me_plan_trial.json";
import stripe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";
import apple from "../../../ios/RivoltTests/Fixtures/me_plan_apple.json";
import founding from "../../../ios/RivoltTests/Fixtures/me_plan_founding.json";
import lapsed from "../../../ios/RivoltTests/Fixtures/me_plan_lapsed.json";
import cancelled from "../../../ios/RivoltTests/Fixtures/me_plan_cancelled.json";
import grace from "../../../ios/RivoltTests/Fixtures/me_plan_grace.json";
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

  it("tells free, each paid source and a founding year apart", () => {
    expect(planState(free)).toEqual({ kind: "free" });
    // No new account gets a trial; an old trial row reads as Pro granted to the account, with nothing about a trial.
    expect(planState(trial)).toMatchObject({ kind: "pro", source: "grant" });
    expect(planState(stripe)).toMatchObject({ kind: "pro", source: "stripe", periodEnd: "2026-12-01" });
    expect(planState(apple)).toMatchObject({ kind: "pro", source: "apple", periodEnd: "2026-12-01T00:00:00Z" });
    expect(planState(founding)).toMatchObject({ kind: "pro", source: "founding", periodEnd: "2027-11-01" });
    expect(planState({ ...stripe, plan_source: "something-new" })).toMatchObject({ kind: "pro", source: "grant" });
  });

  it("takes a lapse from plan_ended_at, not from a date in the past", () => {
    expect(planState(lapsed)).toEqual({ kind: "lapsed", endedOn: "2026-10-01", endedSource: "stripe" });
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

  it("carries a cancellation and a failed payment on the plan", () => {
    expect(planState(cancelled)).toMatchObject({ kind: "pro", source: "stripe", cancelsAt: "2026-12-01", graceEnds: null });
    expect(planState(grace)).toMatchObject({ kind: "pro", source: "stripe", cancelsAt: null, graceEnds: "2026-12-08" });
  });
});

describe("B8: one billing instant, one date everywhere", () => {
  const base = { plan: "pro", plan_source: "stripe", billing_enabled: true, history_days: null } as const;
  it("shows the UTC calendar date for a utc_date plan, in any zone", () => {
    // 2026-12-01T00:00:00Z is Nov 30 in Pacific: the card must still say December 1 (what Stripe's Portal says).
    const st = planState({ ...base, plan_date_basis: "utc_date", plan_period_end: "2026-12-01T00:00:00Z", plan_period_end_date: "2026-12-01", plan_cancels_at: "2026-12-01T00:00:00Z", plan_cancels_date: "2026-12-01" } as never);
    expect(st).toMatchObject({ kind: "pro", periodEnd: "2026-12-01", cancelsAt: "2026-12-01" });
    expect(planDate("2026-12-01")).toBe(new Date("2026-12-01T12:00:00Z").toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric", timeZone: "UTC" }));
    expect(planDate("2026-12-01")).toContain("1");
    expect(planDate("2026-12-01")).not.toContain("30");
  });
  it("keeps the instant for an instant (Apple) plan and for a server that sends no dates", () => {
    const apple = planState({ ...base, plan_source: "apple", plan_date_basis: "instant", plan_period_end: "2026-12-01T00:00:00Z" } as never);
    expect(apple).toMatchObject({ periodEnd: "2026-12-01T00:00:00Z" });
    const old = planState({ ...base, plan_period_end: "2026-12-01T00:00:00Z" } as never);
    expect(old).toMatchObject({ periodEnd: "2026-12-01T00:00:00Z" });
  });
  it("a lapse reads the ended date", () => {
    const st = planState({ plan: "free", billing_enabled: true, history_days: 90, plan_date_basis: "utc_date", plan_ended_at: "2026-11-06T02:52:00Z", plan_ended_date: "2026-11-06", plan_ended_source: "stripe" } as never);
    expect(st).toMatchObject({ kind: "lapsed", endedOn: "2026-11-06" });
  });
});
