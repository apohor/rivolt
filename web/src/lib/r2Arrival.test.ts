import { describe, expect, it } from "vitest";
import { arrivalMonthChoices, arrivalMonthLabel } from "./r2Arrival";

describe("R2 arrival months", () => {
  it("offers this month through 24 months ahead, as the server accepts", () => {
    const c = arrivalMonthChoices(new Date(2026, 9, 31));
    expect(c).toHaveLength(25);
    expect(c[0]).toEqual({ value: "2026-10", label: "October 2026" });
    expect(c[3].value).toBe("2027-01");
    expect(c[24].value).toBe("2028-10");
  });

  it("never shows a malformed month", () => {
    expect(arrivalMonthLabel("2026-12")).toBe("December 2026");
    expect(arrivalMonthLabel("2026-13")).toBe("");
    expect(arrivalMonthLabel(null)).toBe("");
  });
});
