import { describe, expect, it } from "vitest";
import outside from "../../../ios/RivoltTests/Fixtures/err_outside_free_window.json";
import required from "../../../ios/RivoltTests/Fixtures/err_pro_required.json";
import { ApiError } from "./api";
import { proLimitOf, proLimitText } from "./proLimit";

describe("a free account's limits (HTTP 402)", () => {
  it("reads the code, from the server's bodies", () => {
    expect(proLimitOf(new ApiError(402, outside))).toEqual({ code: "outside_free_window", freeDays: 90 });
    expect(proLimitOf(new ApiError(402, required))).toEqual({ code: "pro_required" });
  });

  it("ignores everything else, above all a 403 (terms or signed out), which is not a paywall", () => {
    expect(proLimitOf(new ApiError(403, outside))).toBeNull();
    expect(proLimitOf(new ApiError(402, { error: "something_else" }))).toBeNull();
    expect(proLimitOf(new ApiError(402, "payment required"))).toBeNull();
    expect(proLimitOf(new Error("x"))).toBeNull();
  });

  it("says why and that nothing is lost", () => {
    expect(proLimitText({ code: "outside_free_window", freeDays: 90 })).toMatch(/last 90 days.*kept.*export/);
    expect(proLimitText({ code: "pro_required" })).toMatch(/kept either way/);
  });
});
