import { afterEach, describe, expect, it, vi } from "vitest";
import { forgetSigninEmail, providerToRestart, readSigninEmail, readSigninProvider, rememberSigninEmail, rememberSigninProvider } from "./signinDraft";

afterEach(() => {
  window.sessionStorage.clear();
  vi.restoreAllMocks();
});

describe("sign-in draft", () => {
  it("remembers a trimmed email and forgets it", () => {
    rememberSigninEmail("  me@example.com ");
    expect(readSigninEmail()).toBe("me@example.com");
    forgetSigninEmail();
    expect(readSigninEmail()).toBe("");
  });

  it("remembers the provider chosen", () => {
    rememberSigninProvider("hydra");
    expect(readSigninProvider()).toBe("hydra");
  });

  it("restarts only a provider the server still lists", () => {
    const list = [{ name: "hydra" }, { name: "google" }];
    expect(providerToRestart(list, "google")).toEqual({ name: "google" });
    expect(providerToRestart(list, "github")).toBeNull();
    expect(providerToRestart(list, "")).toBeNull();
    expect(providerToRestart(null, "hydra")).toBeNull();
  });

  it("does nothing when storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(() => rememberSigninEmail("a@b.c")).not.toThrow();
    expect(readSigninEmail()).toBe("");
  });
});
