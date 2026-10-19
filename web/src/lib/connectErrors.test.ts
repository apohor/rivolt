import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { connectFailure } from "./connectErrors";

const err = (status: number, body: unknown) => new ApiError(status, body);

describe("connectFailure", () => {
  it("a rejected login explains the likely causes and tracks as user-action", () => {
    const f = connectFailure(err(401, { error: "Rivian didn't accept that email or password. Double-check both", class: "user_action" }), "password");
    expect(f.cls).toBe("user_action");
    expect(f.title).toBe("Rivian didn't accept that sign-in");
    expect(f.body).toContain("accepted your invite");
    expect(f.track).toBe("user-action");
  });
  it("Rivian being slow or down says it is not the user's details, and never shows the upstream text", () => {
    for (const cls of ["transient", "outage"]) {
      const f = connectFailure(err(503, { error: "graphql: INTERNAL_SERVER_ERROR at gateway", class: cls }), "password");
      expect(f.title).toBe("Rivian isn't answering right now");
      expect(f.body).not.toMatch(/graphql|gateway/i);
      expect(f.track).toBe("other");
    }
  });
  it("rate limiting says to wait and tracks as rate", () => {
    const f = connectFailure(err(429, { error: "x", class: "rate_limited" }), "code");
    expect(f.title).toBe("Too many tries");
    expect(f.track).toBe("rate");
  });
  it("a bad one-time code says codes expire, and tracks as mfa", () => {
    const f = connectFailure(err(401, { error: "bad otp", class: "user_action" }), "code");
    expect(f.title).toBe("That code didn't work");
    expect(f.track).toBe("mfa");
  });
  it("anything else, including a plain network error, gets a generic line", () => {
    expect(connectFailure(new Error("Failed to fetch"), "password")).toMatchObject({ cls: "unknown", title: "Couldn't sign in", track: "other" });
    expect(connectFailure(err(500, "boom"), "password").cls).toBe("unknown");
  });
});
