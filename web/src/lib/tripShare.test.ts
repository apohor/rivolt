import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { fetchSharedTrip, readShareToken, shareCreateMessage, shareLinkState } from "./tripShare";

const TOKEN = "abcdefghijklmnopqrstuv";

afterEach(() => {
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

describe("readShareToken", () => {
  it("takes the token and clears the hash, and a second call still has it", () => {
    window.history.replaceState(null, "", `/s#${TOKEN}`);
    expect(readShareToken()).toBe(TOKEN);
    expect(window.location.hash).toBe("");
    expect(window.location.pathname).toBe("/s");
    expect(readShareToken()).toBe(TOKEN);
  });
  it("clears a wrong-looking token and returns none", () => {
    window.history.replaceState(null, "", "/s#nope");
    expect(readShareToken()).toBeNull();
    expect(window.location.hash).toBe("");
  });
});

describe("fetchSharedTrip", () => {
  const answer = (status: number, body: unknown = {}, headers: Record<string, string> = {}) =>
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), { status, headers })));
  it("sends the token in the body with no cookies", async () => {
    answer(404);
    await fetchSharedTrip(TOKEN);
    const [url, init] = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(url).toBe("/api/public/trips/share");
    expect(init.credentials).toBe("omit");
    expect(JSON.parse(init.body)).toEqual({ token: TOKEN });
    expect(String(url)).not.toContain(TOKEN);
  });
  it("maps the answers", async () => {
    answer(404);
    expect((await fetchSharedTrip(TOKEN)).kind).toBe("notfound");
    answer(429, {}, { "Retry-After": "30" });
    expect(await fetchSharedTrip(TOKEN)).toEqual({ kind: "limited", retryAfter: 30 });
    answer(503, {}, { "Retry-After": "5" });
    expect(await fetchSharedTrip(TOKEN)).toEqual({ kind: "busy", retryAfter: 5 });
    answer(200, { nonsense: true });
    expect((await fetchSharedTrip(TOKEN)).kind).toBe("error");
  });
});

describe("create errors and link states", () => {
  it("says something kind for each code, never the raw error", () => {
    expect(shareCreateMessage(new ApiError(409, { code: "unsupported" }))).toMatch(/multi-day/);
    expect(shareCreateMessage(new ApiError(409, { code: "too_many_links" }))).toMatch(/25/);
    expect(shareCreateMessage(new ApiError(422, { code: "too_short" }))).toMatch(/too short/);
    expect(shareCreateMessage(new ApiError(429, { code: "rate_limited" }))).toMatch(/Too many/);
    expect(shareCreateMessage(new ApiError(500, "boom"))).toBe("Couldn't create the link. Try again.");
  });
  it("names a link live, expired or revoked", () => {
    expect(shareLinkState({ expires_at: null, revoked_at: null })).toBe("live");
    expect(shareLinkState({ expires_at: "2020-01-01T00:00:00Z", revoked_at: null })).toBe("expired");
    expect(shareLinkState({ expires_at: null, revoked_at: "2026-01-01T00:00:00Z" })).toBe("revoked");
  });
});
