import { describe, expect, it } from "vitest";
import { coverage, fmtClock, hasErrors, toWindows, validateWindows, type DraftWindow } from "./offpeak";

const w = (over: Partial<DraftWindow> = {}): DraftWindow => ({ days: "all", start: "21:00", end: "06:00", rate: "0.09", label: "", ...over });

describe("validateWindows", () => {
  it("accepts a clean overnight window", () => {
    expect(hasErrors(validateWindows([w()]))).toBe(false);
  });
  it("flags a rate that is missing, out of range or too precise", () => {
    expect(validateWindows([w({ rate: "" })])[0].rate).toBe("Enter a rate.");
    expect(validateWindows([w({ rate: "5.01" })])[0].rate).toContain("between 0 and 5");
    expect(validateWindows([w({ rate: "-1" })])[0].rate).toContain("between 0 and 5");
    expect(validateWindows([w({ rate: "0.12345" })])[0].rate).toBe("Use at most 4 decimal places.");
    expect(validateWindows([w({ rate: "0" })])[0].rate).toBeUndefined();
    expect(validateWindows([w({ rate: "0.0900" })])[0].rate).toBeUndefined();
  });
  it("flags equal start and end", () => {
    expect(validateWindows([w({ start: "08:00", end: "08:00" })])[0].time).toContain("can't be the same");
  });
  it("flags an overlap on the later row, naming the earlier", () => {
    const e = validateWindows([w({ days: "weekdays" }), w({ days: "all", start: "05:00", end: "07:00" })]);
    expect(e[0].overlap).toBeUndefined();
    expect(e[1].overlap).toContain("Overlaps window 1");
  });
  it("lets back-to-back windows touch, and weekday and weekend windows share hours", () => {
    expect(hasErrors(validateWindows([w({ start: "21:00", end: "06:00" }), w({ start: "06:00", end: "08:00" })]))).toBe(false);
    expect(hasErrors(validateWindows([w({ days: "weekdays" }), w({ days: "weekends" })]))).toBe(false);
  });
  it("a window past midnight on Sunday runs into Monday morning", () => {
    const e = validateWindows([w({ days: "weekends", start: "22:00", end: "02:00" }), w({ days: "weekdays", start: "00:00", end: "01:00" })]);
    expect(e[1].overlap).toContain("Overlaps window 1");
  });
  it("flags an overlong name", () => {
    expect(validateWindows([w({ label: "x".repeat(41) })])[0].label).toContain("at most 40");
  });
});

describe("helpers", () => {
  it("formats clock times", () => {
    expect(fmtClock("21:00")).toBe("9 PM");
    expect(fmtClock("00:30")).toBe("12:30 AM");
    expect(fmtClock("12:00")).toBe("12 PM");
  });
  it("covers hours on a weekday", () => {
    const c = coverage([w({ days: "weekdays" })], "weekdays");
    expect(c[21] && c[23] && c[0] && c[5]).toBe(true);
    expect(c[6] || c[12] || c[20]).toBe(false);
  });
  it("sends rates to four places and trims the name", () => {
    expect(toWindows([w({ rate: "0.09", label: "  Night " })])).toEqual([{ days: "all", start: "21:00", end: "06:00", rate: 0.09, label: "Night" }]);
  });
});

import { serverFieldError } from "./offpeak";
describe("serverFieldError", () => {
  it("puts a refusal on its row and field", () => {
    expect(serverFieldError({ error: "overlaps window 1", field: "windows[1].start" })).toEqual({ row: 1, field: "start", message: "overlaps window 1" });
    expect(serverFieldError({ error: "bad zone", field: "tz" })).toEqual({ row: null, field: "tz", message: "bad zone" });
    expect(serverFieldError({ error: "too many", field: "windows" })).toEqual({ row: null, field: "windows", message: "too many" });
    expect(serverFieldError({ error: "x" })).toBeNull();
  });
});
