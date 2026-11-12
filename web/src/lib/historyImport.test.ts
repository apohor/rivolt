import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { addedSummary, importFailure, skipLabel, undoWarning, warningText } from "./historyImport";

describe("history import copy", () => {
  it("says what was added and what undo removes", () => {
    expect(addedSummary(1, 2)).toBe("Added 1 drive, 2 charges.");
    expect(undoWarning(12, 1)).toBe("This removes 12 drives and 1 charge. Notes or tags you added to them go too.");
  });
  it("labels skip reasons and warnings", () => {
    expect(skipLabel("overlaps_live")).toBe("already recorded by your vehicle");
    expect(skipLabel("odd_one")).toBe("odd one");
    expect(warningText("ambiguous_local_times")).toMatch(/clocks change/);
    expect(warningText("unknown")).toBe("");
  });
  it("shows header names only for unrecognized files", () => {
    const f = importFailure(new ApiError(422, { error: "x", code: "unknown_columns", header: ["a", "b"] }));
    expect(f.header).toEqual(["a", "b"]);
  });
  it("a stopped commit carries its import id", () => {
    expect(importFailure(new ApiError(500, { error: "x", code: "stopped", id: "i1" })).importID).toBe("i1");
  });
  it("maps codes, and carries what the file does not say", () => {
    expect(importFailure(new ApiError(409, { error: "x", code: "preview_stale" })).text).toMatch(/changed/);
    const n = importFailure(new ApiError(422, { error: "x", code: "need_choice", need: ["distance_unit"] }));
    expect(n.need).toEqual(["distance_unit"]);
    expect(importFailure(new ApiError(429, { code: "rate_limited" })).text).toMatch(/Too many/);
  });
});
