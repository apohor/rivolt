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

  it("never redirects on a 401 from the Hydra sign-in (the password form or a passkey)", async () => {
    mockFetch(401, "invalid credentials", "text/plain");
    for (const url of ["/api/auth/hydra/login", "/api/auth/hydra/passkey/finish", "/api/auth/hydra/passkey/begin"]) {
      await expect(api.post(url, {})).rejects.toBeInstanceOf(ApiError);
    }
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
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, pathname: "/", search: "", replace, assign });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("unauthorized", { status: 401, headers: { "X-Rivolt-Auth-Error": "signups_disabled" } })),
    );
    const { api } = await import("./api");
    await expect(api.get("/api/auth/me")).rejects.toBeTruthy();
    expect(replace).toHaveBeenCalledWith("/login?error=signups_disabled");
    expect(assign).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("does not loop when the error is already in the URL", async () => {
    const replace = vi.fn();
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, pathname: "/login", search: "?error=signups_disabled", replace, assign });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("unauthorized", { status: 401, headers: { "X-Rivolt-Auth-Error": "signups_disabled" } })),
    );
    const { api } = await import("./api");
    await expect(api.get("/api/auth/me")).rejects.toBeTruthy();
    expect(replace).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
});

describe("whoami", () => {
  afterEach(() => vi.unstubAllGlobals());
  const reply = (status: number, body: unknown) =>
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), { status })));

  it("is null for 200 {authenticated:false}", async () => {
    reply(200, { authenticated: false });
    const { backend } = await import("./api");
    expect(await backend.whoami()).toBeNull();
  });
  it("is null for a 401 (expired session)", async () => {
    reply(401, { error: "unauthorized" });
    const { backend } = await import("./api");
    expect(await backend.whoami()).toBeNull();
  });
  it("returns the user when signed in", async () => {
    reply(200, { authenticated: true, user_id: "u1", username: "a@b.c", role: "user", onboarding_completed: true });
    const { backend } = await import("./api");
    expect((await backend.whoami())?.user_id).toBe("u1");
  });
});

// The signed-out demo has no session: calling a session-only endpoint
// would 401 into the console. liveDrive must stay local there.
describe("liveDrive in the demo", () => {
  afterEach(() => vi.restoreAllMocks());

  it("makes no request on /demo and returns null", async () => {
    Object.defineProperty(window, "location", {
      value: { pathname: "/demo", search: "", assign: vi.fn() },
      writable: true,
    });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const { backend } = await import("./api");
    await expect(backend.liveDrive("demo-vin")).resolves.toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("still asks the server outside the demo", async () => {
    Object.defineProperty(window, "location", {
      value: { pathname: "/", search: "", assign: vi.fn() },
      writable: true,
    });
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 204,
      headers: { get: () => "application/json" },
      text: async () => "",
    });
    vi.stubGlobal("fetch", fetchMock);
    const { backend } = await import("./api");
    await expect(backend.liveDrive("vin")).resolves.toBeNull();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("backend.samples vehicle scope", () => {
  afterEach(() => vi.restoreAllMocks());
  it("passes ?vehicle= when given and omits it otherwise", async () => {
    Object.defineProperty(window, "location", { value: { pathname: "/", search: "", assign: vi.fn() }, writable: true });
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => "[]",
    });
    vi.stubGlobal("fetch", fetchMock);
    const { backend } = await import("./api");
    const a = new Date("2026-01-01T00:00:00Z");
    const b = new Date("2026-01-01T01:00:00Z");
    await backend.samples(a, 100, b, "veh-1");
    await backend.samples(a, 100, b);
    expect(String(fetchMock.mock.calls[0][0])).toContain("vehicle=veh-1");
    expect(String(fetchMock.mock.calls[1][0])).not.toContain("vehicle=");
  });
});

describe("backend.sleepActivity range", () => {
  afterEach(() => vi.restoreAllMocks());
  it("sends until only when given", async () => {
    Object.defineProperty(window, "location", { value: { pathname: "/", search: "", assign: vi.fn() }, writable: true });
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => "[]" });
    vi.stubGlobal("fetch", fetchMock);
    const { backend } = await import("./api");
    await backend.sleepActivity("2026-10-01T00:00:00Z", "veh", "2026-11-01T00:00:00Z");
    await backend.sleepActivity("2026-10-01T00:00:00Z");
    expect(String(fetchMock.mock.calls[0][0])).toMatch(/since=.*&until=.*&vehicle=veh/);
    expect(String(fetchMock.mock.calls[1][0])).not.toContain("until=");
  });
});
