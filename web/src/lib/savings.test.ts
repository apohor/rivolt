import { describe, expect, it } from "vitest";
import { leftOutLine, offpeakShareOf, savedHeadline, savingsView } from "./savings";

describe("savingsView", () => {
  it("says what is missing instead of a number", () => {
    expect(savingsView({ needs: "offpeak_schedule" })).toEqual({ kind: "needs", need: "schedule" });
    expect(savingsView({ needs: "home_rate" })).toEqual({ kind: "needs", need: "home_rate" });
    expect(savingsView({ needs: "more_home_sessions" })).toEqual({ kind: "needs", need: "sessions" });
    expect(savingsView({ could_have_saved: null })).toEqual({ kind: "needs", need: "sessions" });
  });
  it("a flat-rate answer is its own state and holds no estimate", () => {
    const flat = { flat_rate: true, reason: "flat_rate", basis: "You said your electricity is one flat rate." };
    expect(savingsView(flat)).toEqual({ kind: "flat", basis: "You said your electricity is one flat rate." });
    // The share card's off-peak line needs a number here; a flat-rate answer has none.
    expect(typeof (flat as { offpeak_share_pct?: unknown }).offpeak_share_pct).not.toBe("number");
    // Even next to stale-looking fields, the reason wins.
    expect(savingsView({ reason: "flat_rate", could_have_saved: 12 }).kind).toBe("flat");
  });
  it("a saving carries the counts and the reasons, biggest first", () => {
    const v = savingsView({ could_have_saved: 12.4, sessions_moved: 9, sessions_left_out: 3, left_out_reasons: { plugged_in_too_briefly: 1, no_cheaper_hours: 2 }, sessions_considered: 12, basis: "From your home charges.", as_charged_total: 40, if_moved_total: 27.6 });
    expect(v).toEqual({ kind: "saved", saved: 12.4, moved: 9, leftOut: 3, asCharged: 40, ifMoved: 27.6, reasons: ["already at the cheapest hours it could use (2)", "plugged in too briefly for the charge to fit in one stretch (1)"], considered: 12, basis: "From your home charges." });
  });
  it("no saving is its own state, not a zero dollar claim", () => {
    expect(savingsView({ could_have_saved: 0, sessions_moved: 0, sessions_left_out: 2 })).toMatchObject({ kind: "nothing", leftOut: 2, considered: null });
  });
});

describe("wording", () => {
  it("is an estimate and says could have saved", () => {
    expect(savedHeadline(9, "$12.40")).toBe("Moving 9 home charges into your off-peak windows could have saved about $12.40 (estimate).");
    expect(savedHeadline(1, "$1.00")).toContain("1 home charge into");
    expect(savedHeadline(9, "$12.40")).not.toMatch(/overpaid|wasted/i);
  });
  it("says what was left out", () => {
    expect(leftOutLine(3)).toBe("3 sessions could not be moved and are left out.");
    expect(leftOutLine(1)).toBe("1 session could not be moved and is left out.");
  });
});

describe("the share card's off-peak line", () => {
  it("is offered only when the server gave a share, never for a flat-rate user", () => {
    expect(offpeakShareOf({ offpeak_share_pct: 61.6 })).toBe(61.6);
    expect(offpeakShareOf({ flat_rate: true, reason: "flat_rate" })).toBeNull();
    expect(offpeakShareOf({ needs: "offpeak_schedule" })).toBeNull();
    expect(offpeakShareOf(undefined)).toBeNull();
  });
});
