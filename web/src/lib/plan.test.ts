import { describe, expect, it } from "vitest";
import { daysLeft, planDate, planState } from "./plan";

const now = new Date("2026-12-10T12:00:00Z");

describe("planState", () => {
  it("is off wherever the server does not bill", () => {
    expect(planState(null, now)).toEqual({ kind: "off" });
    expect(planState({}, now)).toEqual({ kind: "off" });
  });

  it("tells a trial, a paid plan and a founding year apart", () => {
    expect(planState({ plan: "pro", source: "trial", trial_ends: "2026-12-20T00:00:00Z" }, now)).toEqual({ kind: "trial", ends: "2026-12-20T00:00:00Z" });
    expect(planState({ plan: "pro", source: "stripe", period_end: "2027-01-05T00:00:00Z" }, now)).toEqual({ kind: "pro", source: "stripe", periodEnd: "2027-01-05T00:00:00Z" });
    expect(planState({ plan: "pro", source: "apple", period_end: null }, now)).toEqual({ kind: "pro", source: "apple", periodEnd: null });
    expect(planState({ plan: "pro", source: "founding", period_end: "2027-11-01T00:00:00Z" }, now).kind).toBe("pro");
    expect(planState({ plan: "pro", source: "something-new" }, now)).toMatchObject({ kind: "pro", source: "grant" });
  });

  it("calls a free account that never had Pro 'free', and one whose period or trial ended 'lapsed'", () => {
    expect(planState({ plan: "free" }, now)).toEqual({ kind: "free" });
    expect(planState({ plan: "free", period_end: "2027-03-01T00:00:00Z" }, now)).toEqual({ kind: "free" });
    expect(planState({ plan: "free", period_end: "2026-12-01T00:00:00Z" }, now)).toEqual({ kind: "lapsed", endedOn: "2026-12-01T00:00:00.000Z" });
    expect(planState({ plan: "free", trial_ends: "2026-11-20T00:00:00Z" }, now)).toEqual({ kind: "lapsed", endedOn: "2026-11-20T00:00:00.000Z" });
    expect(planState({ plan: "free", trial_ends: "2026-11-20T00:00:00Z", period_end: "2026-12-02T00:00:00Z" }, now)).toMatchObject({ endedOn: "2026-12-02T00:00:00.000Z" });
  });

  it("never shows a malformed date", () => {
    expect(planDate("nonsense")).toBe("");
    expect(planDate(null)).toBe("");
    expect(planDate("2026-12-20T12:00:00Z")).toContain("2026");
    expect(daysLeft("2026-12-13T12:00:00Z", now)).toBe(3);
    expect(daysLeft("2026-12-01T00:00:00Z", now)).toBe(0);
    expect(daysLeft("x", now)).toBeNull();
  });
});
