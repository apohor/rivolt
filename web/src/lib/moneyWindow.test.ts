import { describe, expect, it } from "vitest";
import { csvIsPro, monthsBack } from "./moneyWindow";

describe("a free account's Money window", () => {
  it("counts calendar months back", () => {
    expect(monthsBack("2026-10", "2026-10")).toBe(0);
    expect(monthsBack("2026-08", "2026-10")).toBe(2);
    expect(monthsBack("2026-07", "2026-10")).toBe(3);
    expect(monthsBack("2025-12", "2026-02")).toBe(2);
    expect(monthsBack("junk", "2026-10")).toBe(0);
  });

  it("makes the CSV of a month outside the window a Pro download, only for a free or lapsed account", () => {
    // 12 months back is inside the window (the current month and the 12 before it); 13 is outside.
    expect(csvIsPro({ kind: "free", months: 12 }, "2025-10", "2026-10")).toBe(false);
    expect(csvIsPro({ kind: "free", months: 12 }, "2025-09", "2026-10")).toBe(true);
    // The server's length wins: a new account's 6 months.
    expect(csvIsPro({ kind: "free", months: 6 }, "2026-04", "2026-10")).toBe(false);
    expect(csvIsPro({ kind: "free", months: 6 }, "2026-03", "2026-10")).toBe(true);
    // Not stated: the 12 every account had before the setting.
    expect(csvIsPro({ kind: "free" }, "2025-10", "2026-10")).toBe(false);
    expect(csvIsPro({ kind: "free" }, "2025-09", "2026-10")).toBe(true);
    expect(csvIsPro({ kind: "lapsed", endedOn: null, endedSource: null }, "2024-01", "2026-10")).toBe(true);
    expect(csvIsPro({ kind: "lapsed", endedOn: null, endedSource: null, months: 12 }, "2026-01", "2026-10")).toBe(false);
    expect(csvIsPro({ kind: "off" }, "2020-01", "2026-10")).toBe(false);
    expect(csvIsPro({ kind: "pro", source: "stripe", periodEnd: null }, "2020-01", "2026-10")).toBe(false);
  });
});
