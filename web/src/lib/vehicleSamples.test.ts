import { describe, expect, it } from "vitest";
import type { Sample } from "./api";
import { forVehicle } from "./vehicleSamples";

const s = (VehicleID: string, n: number) => ({ VehicleID, At: `2026-01-01T00:00:${String(n).padStart(2, "0")}Z` }) as Sample;

describe("forVehicle", () => {
  const mixed = [s("A", 1), s("B", 2), s("A", 3), s("B", 4)];
  it("keeps only the drive's own vehicle, in order", () => {
    expect(forVehicle(mixed, "A").map((x) => x.At.slice(17, 19))).toEqual(["01", "03"]);
  });
  it("never blanks the page when nothing matches or the id is missing", () => {
    expect(forVehicle(mixed, "Z")).toBe(mixed);
    expect(forVehicle(mixed, "")).toBe(mixed);
  });
});
