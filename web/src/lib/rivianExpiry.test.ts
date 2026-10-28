import { describe, expect, it } from "vitest";
import { expiryDate, expiryNotice } from "./rivianExpiry";

describe("rivian expiry wording", () => {
  it("writes the date as Nov 3", () => {
    expect(expiryDate("2026-11-03T18:00:00Z", "en-US")).toBe("Nov 3");
    expect(expiryDate("", "en-US")).toBe("");
    expect(expiryDate("not a date", "en-US")).toBe("");
    expect(expiryDate(undefined, "en-US")).toBe("");
  });
  it("is the approved line from 7 days, with the date", () => {
    expect(expiryNotice(7, "2026-11-03T18:00:00Z", "en-US")).toBe("Your Rivian connection expires Nov 3.");
    expect(expiryNotice(8, "2026-11-03T18:00:00Z", "en-US")).toBeNull();
    expect(expiryNotice(undefined, "2026-11-03T18:00:00Z", "en-US")).toBeNull();
  });
  it("falls back to the days left when the date is unreadable", () => {
    expect(expiryNotice(3, undefined, "en-US")).toBe("Your Rivian connection expires in 3 days.");
    expect(expiryNotice(1, undefined, "en-US")).toBe("Your Rivian connection expires in 1 day.");
    expect(expiryNotice(0, "", "en-US")).toBe("Your Rivian connection expires today.");
  });
});
