import { describe, expect, it } from "vitest";
import type { Vehicle } from "./api";
import { defaultVehicleID, pickerLabel } from "./proVehicles";

const v = (id: string, locked = false) => ({ id, vin: "", name: id, model: "R1S", ...(locked ? { pro_locked: true } : {}) }) as Vehicle;

describe("a free account's vehicles", () => {
  it("never defaults to a locked vehicle", () => {
    expect(defaultVehicleID([v("a", true), v("b")], null)).toBe("b");
    expect(defaultVehicleID([v("a", true), v("b")], "a")).toBe("b");
    expect(defaultVehicleID([v("a"), v("b", true)], "b")).toBe("a");
  });

  it("keeps a stored open choice, and falls back to the first when everything is locked or nothing is stored", () => {
    expect(defaultVehicleID([v("a"), v("b")], "b")).toBe("b");
    expect(defaultVehicleID([v("a"), v("b")], null)).toBe("a");
    expect(defaultVehicleID([v("a", true)], null)).toBe("a");
    expect(defaultVehicleID([], "x")).toBeUndefined();
  });

  it("marks a locked vehicle in a picker", () => {
    expect(pickerLabel("Truck", v("a", true))).toBe("Truck · Pro");
    expect(pickerLabel("Truck", v("a"))).toBe("Truck");
  });
});
