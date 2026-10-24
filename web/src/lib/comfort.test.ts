import { describe, expect, it } from "vitest";
import { cabinValue, comfortParts } from "./comfort";

const base = { cabin_temp_c: 0, cabin_preconditioning_status: "", pet_mode_status: "", cabin_hold_status: "" };

describe("comfortParts", () => {
  it("is the truck's own words in a fixed order", () => {
    expect(comfortParts({ cabin_temp_c: 20, cabin_preconditioning_status: "preconditioning", pet_mode_status: "on", cabin_hold_status: "active" })).toEqual([
      "Preconditioning",
      "Pet mode on",
      "Climate hold",
    ]);
    expect(comfortParts({ ...base, cabin_preconditioning_status: "preconditioned" })).toEqual(["Preconditioned"]);
  });

  it("leaves out unknown, off, disabled, faulty and no reading", () => {
    for (const v of ["", "undefined", "unknown", "off", "disabled", "faulty", "not_preconditioning", "false", "0", "garbage"]) {
      expect(comfortParts({ cabin_temp_c: 0, cabin_preconditioning_status: v, pet_mode_status: v, cabin_hold_status: v })).toEqual([]);
    }
    expect(comfortParts({ ...base, cabin_temp_c: 120 })).toEqual([]);
  });

  it("says nothing about the animal's safety or a limit", () => {
    const text = comfortParts({ ...base, pet_mode_status: "on" }).join(" ").toLowerCase();
    expect(text).toBe("pet mode on");
    for (const banned of ["safe", "limit", "warning", "alert"]) expect(text).not.toContain(banned);
  });
});

describe("cabinValue", () => {
  it("is whole degrees in the chosen unit, and null without a reading", () => {
    expect(cabinValue({ cabin_temp_c: 28.6 }, "c")).toBe("29 °C");
    expect(cabinValue({ cabin_temp_c: 20 }, "f")).toBe("68 °F");
    expect(cabinValue({ cabin_temp_c: 0 }, "c")).toBeNull();
    expect(cabinValue({ cabin_temp_c: 120 }, "c")).toBeNull();
  });
});
