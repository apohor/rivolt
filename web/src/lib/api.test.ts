import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "./api";

// The global 401 handler must distinguish a Rivolt-session failure
// (bounce to /login) from a Rivian-upstream needs_reauth 401 (carries
// an upstream-error `class` field — the page handles it, no redirect).
// Conflating them looped authenticated users with a stale Rivian
// session endlessly between / and /login (issue #40).
describe("api 401 handling", () => {
  let assign: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    assign = vi.fn();
    // jsdom's location is read-only; replace just the assign spy.
    Object.defineProperty(window, "location", {
      value: { pathname: "/", search: "", assign },
      writable: true,
    });
  });
  afterEach(() => vi.restoreAllMocks());

  function mockFetch(status: number, body: unknown, contentType: string) {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: status >= 200 && status < 300,
        status,
        headers: { get: () => contentType },
        text: async () =>
          typeof body === "string" ? body : JSON.stringify(body),
      }),
    );
  }

  it("redirects to /login on a plain app-auth 401", async () => {
    mockFetch(401, "unauthorized", "text/plain");
    await expect(api.get("/api/drives")).rejects.toBeInstanceOf(ApiError);
    expect(assign).toHaveBeenCalledWith("/login");
  });

  it("does NOT redirect on a Rivian-upstream 401 (needs_reauth)", async () => {
    mockFetch(
      401,
      { error: "re-authentication required", class: "user_action" },
      "application/json",
    );
    await expect(api.get("/api/vehicles")).rejects.toBeInstanceOf(ApiError);
    expect(assign).not.toHaveBeenCalled();
  });

  it("never redirects on /api/auth/me", async () => {
    mockFetch(401, "unauthorized", "text/plain");
    await expect(api.get("/api/auth/me")).rejects.toBeInstanceOf(ApiError);
    expect(assign).not.toHaveBeenCalled();
  });
});

describe("signups_disabled auth header", () => {
  it("redirects a 401 carrying X-Rivolt-Auth-Error once to the login error", async () => {
    const replace = vi.fn();
    vi.stubGlobal("location", { ...window.location, pathname: "/", search: "", replace, assign: vi.fn() });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("unauthorized", { status: 401, headers: { "X-Rivolt-Auth-Error": "signups_disabled" } })),
    );
    const { api } = await import("./api");
    await expect(api.get("/api/auth/me")).rejects.toBeTruthy();
    expect(replace).toHaveBeenCalledWith("/login?error=signups_disabled");
    vi.unstubAllGlobals();
  });

  it("does not loop when the error is already in the URL", async () => {
    const replace = vi.fn();
    vi.stubGlobal("location", { ...window.location, pathname: "/login", search: "?error=signups_disabled", replace, assign: vi.fn() });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("unauthorized", { status: 401, headers: { "X-Rivolt-Auth-Error": "signups_disabled" } })),
    );
    const { api } = await import("./api");
    await expect(api.get("/api/auth/me")).rejects.toBeTruthy();
    expect(replace).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
});
