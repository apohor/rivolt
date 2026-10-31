import { describe, expect, it } from "vitest";
import { priceMonth } from "./priceMonth";

describe("priceMonth", () => {
  it("formats a YYYY-MM month and ignores anything else", () => {
    expect(priceMonth("2026-10")).toBe("Oct 2026");
    expect(priceMonth("")).toBe("");
    expect(priceMonth("soon")).toBe("");
    for (const bad of ["2026-13", "2026-00", "2026-99"]) expect(priceMonth(bad)).toBe("");
    expect(priceMonth(undefined)).toBe("");
  });
});
