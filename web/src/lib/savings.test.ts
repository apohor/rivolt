import { describe, expect, it } from "vitest";
import { leftOutLine, savedHeadline, savingsView } from "./savings";

describe("savingsView", () => {
  it("says what is missing instead of a number", () => {
    expect(savingsView({ needs: "offpeak_schedule" })).toEqual({ kind: "needs", need: "schedule" });
    expect(savingsView({ needs: "home_rate" })).toEqual({ kind: "needs", need: "home_rate" });
    expect(savingsView({ needs: "more_sessions" })).toEqual({ kind: "needs", need: "sessions" });
    expect(savingsView({ could_have_saved: null })).toEqual({ kind: "needs", need: "sessions" });
  });
  it("a saving carries the counts and the reasons, biggest first", () => {
    const v = savingsView({ could_have_saved: 12.4, sessions_moved: 9, sessions_left_out: 3, reasons: { too_short: 1, no_window_fits: 2 }, as_charged_total: 40, if_moved_total: 27.6 });
    expect(v).toEqual({ kind: "saved", saved: 12.4, moved: 9, leftOut: 3, asCharged: 40, ifMoved: 27.6, reasons: ["2 no window fits", "1 too short"] });
  });
  it("no saving is its own state, not a zero dollar claim", () => {
    expect(savingsView({ could_have_saved: 0, sessions_moved: 0, sessions_left_out: 2 })).toMatchObject({ kind: "nothing", leftOut: 2 });
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
