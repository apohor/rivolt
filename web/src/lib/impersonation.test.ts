import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getImpersonationTarget, IMPERSONATE_HEADER, startImpersonation, stopImpersonation } from "./impersonation";

const TARGET = { id: "11111111-2222-3333-4444-555555555555", email: "user@example.com" };

describe("stopImpersonation", () => {
  beforeEach(() => sessionStorage.clear());
  afterEach(() => vi.unstubAllGlobals());

  it("tells the server which user the admin stopped viewing, as the admin, then clears the state", () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    startImpersonation(TARGET);
    stopImpersonation();
    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/admin/impersonation/stop");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ target_id: TARGET.id });
    // As the real admin: an impersonated request would be refused on an admin route.
    expect(Object.keys(init.headers as Record<string, string>)).not.toContain(IMPERSONATE_HEADER);
    expect(init.keepalive).toBe(true);
    expect(getImpersonationTarget()).toBeNull();
  });

  it("clears the state even when the call fails (rejects)", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new Error("offline"))));
    startImpersonation(TARGET);
    expect(() => stopImpersonation()).not.toThrow();
    expect(getImpersonationTarget()).toBeNull();
    await Promise.resolve(); // the swallowed rejection must not surface as unhandled
  });

  it("clears the state even when fetch throws synchronously or the server answers 403", () => {
    vi.stubGlobal("fetch", vi.fn(() => { throw new Error("blocked"); }));
    startImpersonation(TARGET);
    expect(() => stopImpersonation()).not.toThrow();
    expect(getImpersonationTarget()).toBeNull();

    vi.stubGlobal("fetch", vi.fn(async () => new Response("no", { status: 403 })));
    startImpersonation(TARGET);
    expect(() => stopImpersonation()).not.toThrow();
    expect(getImpersonationTarget()).toBeNull();
  });

  it("does not call the server when nobody is being viewed", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    stopImpersonation();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
