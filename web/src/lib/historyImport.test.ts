import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { addedSummary, importErrorText, skipLabel, undoWarning } from "./historyImport";

describe("history import copy", () => {
  it("says what was added and what undo removes", () => {
    expect(addedSummary(1, 2)).toBe("Added 1 drive, 2 charges.");
    expect(undoWarning(12, 1)).toBe("This removes 12 drives and 1 charge. Notes or tags you added to them go too.");
  });
  it("labels skip reasons and falls back to the code", () => {
    expect(skipLabel("covered")).toBe("already recorded by your vehicle");
    expect(skipLabel("odd_one")).toBe("odd one");
  });
  it("shows header names only for unrecognized files", () => {
    const e = importErrorText(new ApiError(422, { error: "unrecognized_headers", headers: ["a", "b"] }));
    expect(e.headers).toEqual(["a", "b"]);
    expect(importErrorText(new ApiError(409, { error: "hash_changed" })).text).toMatch(/changed/);
    expect(importErrorText(new ApiError(413, {})).text).toMatch(/50 MB/);
  });
});
