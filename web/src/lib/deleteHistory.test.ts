import { describe, expect, it } from "vitest";
import { cutoffChoices, deletedSummary } from "./deleteHistory";

describe("delete history before a month", () => {
  it("offers the current month and the 36 before it, newest first", () => {
    const c = cutoffChoices(new Date(Date.UTC(2026, 9, 31, 12)));
    expect(c).toHaveLength(37);
    expect(c[0]).toEqual({ value: "2026-10", label: "October 2026" });
    expect(c[3].value).toBe("2026-07");
    expect(c[10].value).toBe("2025-12");
    expect(c[36].value).toBe("2023-10");
  });

  it("says what was deleted, in plain counts", () => {
    expect(deletedSummary({ drives: 1, charges: 2, samples: 1234 })).toBe("Deleted 1 drive, 2 charges and 1,234 telemetry samples.");
  });
});
