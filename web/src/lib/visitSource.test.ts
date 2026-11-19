import { describe, expect, it } from "vitest";
import { visitSourceProps } from "./visitSource";

describe("visitSourceProps", () => {
  it("keeps the referrer host and utm_source as lower-case tokens", () => {
    expect(visitSourceProps("?utm_source=YouTube", "https://m.youtube.com/watch?v=1")).toEqual({
      referrer_host: "m.youtube.com",
      utm_source: "youtube",
    });
  });
  it("drops anything that is not a token, and bad referrers", () => {
    expect(visitSourceProps("?utm_source=a%20b", "not a url")).toEqual({});
    expect(visitSourceProps("?utm_source=" + "a".repeat(41), "")).toEqual({});
    expect(visitSourceProps("", "")).toEqual({});
  });
});
