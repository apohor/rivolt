import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { addFailureMessage, signInFailureMessage } from "./passkeys";

describe("passkey messages", () => {
  const err = (status: number, error?: string) => new ApiError(status, error ? { error } : null);
  it("say what happened in plain words, never the server's own text", () => {
    expect(addFailureMessage(err(403, "password_incorrect"))).toBe("That password isn't right.");
    expect(addFailureMessage(err(409, "passkeys_need_an_email_account"))).toContain("Google, GitHub or Apple");
    expect(addFailureMessage(err(409, "too_many_passkeys"))).toContain("most passkeys");
    expect(addFailureMessage(err(429))).toContain("Too many attempts");
    expect(addFailureMessage(err(500, "boom"))).toBe("Couldn't add the passkey. Try again.");
    expect(addFailureMessage(new Error("x"))).toBe("Couldn't add the passkey. Try again.");
    for (const e of [err(403, "password_incorrect"), err(409, "passkeys_need_an_email_account"), err(500, "internal_secret")]) {
      expect(addFailureMessage(e)).not.toMatch(/password_incorrect|passkeys_need|internal_secret/);
    }
  });
  it("sign-in failures point back to the password", () => {
    expect(signInFailureMessage(err(401))).toContain("password");
    expect(signInFailureMessage(err(429))).toContain("Too many attempts");
    expect(signInFailureMessage(new Error("x"))).toContain("password");
  });
  it("uses no banned words", () => {
    const all = [addFailureMessage(err(403, "password_incorrect")), addFailureMessage(err(409, "passkeys_need_an_email_account")), signInFailureMessage(err(401))].join(" ");
    for (const w of ["safe", "secure", "OK", "normal", "exact", "measured", "towing"]) expect(all).not.toMatch(new RegExp(`\\b${w}\\b`));
  });
});
