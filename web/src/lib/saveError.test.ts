import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { saveErrorText } from "./saveError";

describe("saveErrorText", () => {
  it("never shows a raw error, only plain words or the server's readable reason", () => {
    const generic = "Couldn't save. Try again in a moment.";
    expect(saveErrorText(new Error("boom"))).toBe(generic);
    expect(saveErrorText(new ApiError(500, "boom"))).toBe(generic);
    expect(saveErrorText(new ApiError(502, "<html><body>Bad gateway</body></html>"))).toBe(generic);
    expect(saveErrorText(new ApiError(400, "plain text body"))).toBe(generic);
    expect(saveErrorText(new ApiError(400, { error: "line one\nstack" }))).toBe(generic);
    expect(saveErrorText(undefined)).toBe(generic);
    expect(saveErrorText(new ApiError(400, { error: "place must be home, away or null" }))).toBe(
      "Couldn't save: Place must be home, away or null.",
    );
    expect(saveErrorText(new ApiError(403, { error: "not available while viewing as another user" }))).toBe(
      "Couldn't save: Not available while viewing as another user.",
    );
  });

  it("says a still-open charge can't be priced yet, only for that 409", () => {
    const open = "This charge is still open, so it can't be priced yet. Rivolt can price it once it ends.";
    expect(saveErrorText(new ApiError(409, "charge still in progress\n"))).toBe(open);
    // Any other conflict keeps the plain words.
    expect(saveErrorText(new ApiError(409, "version conflict"))).toBe("Couldn't save. Try again in a moment.");
    expect(saveErrorText(new ApiError(500, "charge still in progress"))).toBe("Couldn't save. Try again in a moment.");
  });
});
