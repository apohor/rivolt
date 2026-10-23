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
});
