import { describe, expect, it } from "vitest";
import { comfortParts } from "./comfort";

const base = { cabin_temp_c: 0, cabin_preconditioning_status: "", pet_mode_status: "", cabin_hold_status: "" };

describe("comfortParts", () => {
  it("is the truck's own words in a fixed order", () => {
    expect(comfortParts({ cabin_temp_c: 20, cabin_preconditioning_status: "preconditioning", pet_mode_status: "on", cabin_hold_status: "active" }, "f")).toEqual([
      "Cabin 68 °F",
      "Preconditioning",
      "Pet mode on",
      "Climate hold",
    ]);
    expect(comfortParts({ ...base, cabin_preconditioning_status: "preconditioned" }, "c")).toEqual(["Preconditioned"]);
  });

  it("leaves out unknown, off, disabled, faulty and no reading", () => {
    for (const v of ["", "undefined", "unknown", "off", "disabled", "faulty", "not_preconditioning", "false", "0", "garbage"]) {
      expect(comfortParts({ cabin_temp_c: 0, cabin_preconditioning_status: v, pet_mode_status: v, cabin_hold_status: v }, "c")).toEqual([]);
    }
    expect(comfortParts({ ...base, cabin_temp_c: 120 }, "c")).toEqual([]);
  });

  it("says nothing about the animal's safety or a limit", () => {
    const text = comfortParts({ ...base, pet_mode_status: "on" }, "c").join(" ").toLowerCase();
    expect(text).toBe("pet mode on");
    for (const banned of ["safe", "limit", "warning", "alert"]) expect(text).not.toContain(banned);
  });
});
