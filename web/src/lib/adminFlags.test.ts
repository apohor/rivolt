import { describe, expect, it } from "vitest";
import { audienceBody, audienceConfirm, audienceLabel, audienceOf, capLabel, parseCap } from "./adminFlags";

describe("audience flags", () => {
  it("reads everyone, selected users, nobody", () => {
    expect(audienceOf({ all: true })).toBe("everyone");
    expect(audienceOf({ all: true, users: ["a"] })).toBe("everyone");
    expect(audienceOf({ users: ["a", "b"] })).toBe("selected");
    expect(audienceOf({})).toBe("nobody");
    expect(audienceOf(undefined)).toBe("nobody");
    expect(audienceOf({ users: [] })).toBe("nobody");
  });
  it("labels them", () => {
    expect(audienceLabel({ all: true })).toBe("Everyone");
    expect(audienceLabel({ users: ["a"] })).toBe("1 user");
    expect(audienceLabel({ users: ["a", "b"] })).toBe("2 users");
    expect(audienceLabel({})).toBe("Nobody");
  });
  it("builds the request body", () => {
    expect(audienceBody("everyone", ["a"])).toEqual({ all: true, users: [] });
    expect(audienceBody("selected", ["a", "b"])).toEqual({ all: false, users: ["a", "b"] });
    expect(audienceBody("nobody", ["a"])).toEqual({ all: false, users: [] });
  });
  it("asks the right question", () => {
    expect(audienceConfirm("the Money tab", "nobody", "everyone", 0)).toBe("Turn the Money tab on for everyone?");
    expect(audienceConfirm("the Money tab", "everyone", "nobody", 0)).toBe("Turn the Money tab off for everyone?");
    expect(audienceConfirm("the Money tab", "everyone", "selected", 2)).toBe("Limit the Money tab to 2 users (it is on for everyone now)?");
    expect(audienceConfirm("the Money tab", "nobody", "selected", 1)).toBe("Set the Money tab for 1 user?");
  });
});

describe("AI call cap", () => {
  it("labels and parses", () => {
    expect(capLabel(0)).toBe("No cap");
    expect(capLabel(undefined)).toBe("No cap");
    expect(capLabel(1500)).toBe("1,500 calls per user per day");
    expect(parseCap("")).toBeNull();
    expect(parseCap("  ")).toBeNull();
    expect(parseCap("25")).toBe(25);
    expect(parseCap("0")).toBe(0);
    expect(parseCap("-1")).toBeNull();
    expect(parseCap("2.5")).toBeNull();
    expect(parseCap("abc")).toBeNull();
  });
});
