import { describe, expect, it } from "vitest";
import { specKwh } from "./PackHealthCard";

describe("specKwh", () => {
  it("shows a usable-looking spec and hides an unknown or gross one", () => {
    expect(specKwh(131)).toBe(131);
    expect(specKwh(0)).toBe(0);
    expect(specKwh(180)).toBe(0);
  });
});
