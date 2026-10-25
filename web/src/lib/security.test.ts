import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { plural, securityFailure } from "./security";

const err = (status: number, body: object | null) => new ApiError(status, body);

describe("securityFailure", () => {
  it("says each refusal in plain words", () => {
    expect(securityFailure(err(403, { error: "password_incorrect" })).text).toBe("That isn't your current password.");
    expect(securityFailure(err(429, null)).text).toContain("Too many wrong attempts");
    expect(securityFailure(err(400, { error: "new_password_must_differ" })).text).toContain("different");
  });
  it("shows the password policy's reason, capped, and a fallback without one", () => {
    expect(securityFailure(err(422, { error: "password_rejected", reason: "The password is too short." })).text).toContain("too short");
    expect(securityFailure(err(422, { error: "password_rejected" })).text).toContain("less common");
    expect(securityFailure(err(422, { error: "password_rejected", reason: "x".repeat(900) })).text.length).toBeLessThan(260);
  });
  it("marks an account without a Rivolt password so the cards hide", () => {
    const f = securityFailure(err(409, { error: "needs_an_email_account" }));
    expect(f.noPassword).toBe(true);
    expect(f.text).toContain("Google, GitHub or Apple");
  });
  it("tells a changed password with an unfinished sign-out apart, and points at the existing button", () => {
    const f = securityFailure(err(502, { error: "password_changed_but_sign_out_incomplete", password_changed: true }));
    expect(f.signOutIncomplete).toBe(true);
    expect(f.text).toContain("Your password was changed");
    expect(f.text).toContain("Sign out everywhere else");
  });
  it("never shows the server's own error word, and uses no banned word", () => {
    const all = [err(403, { error: "password_incorrect" }), err(409, { error: "needs_an_email_account" }), err(500, { error: "kratos_exploded" })].map((e) => securityFailure(e).text).join(" ");
    expect(all).not.toMatch(/password_incorrect|needs_an_email_account|kratos_exploded/);
    for (const w of ["safe", "secure", "OK", "normal", "exact", "measured", "towing"]) expect(all).not.toMatch(new RegExp(`\\b${w}\\b`));
  });
  it("counts in words", () => {
    expect(plural(1, "passkey", "passkeys")).toBe("1 passkey");
    expect(plural(0, "passkey", "passkeys")).toBe("0 passkeys");
  });
});
