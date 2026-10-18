import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// config.ts caches /api/config at module level, so each case gets a fresh module graph.
async function load(body: unknown, ok = true) {
  vi.resetModules();
  vi.stubGlobal("fetch", vi.fn(async () => (ok ? new Response(JSON.stringify(body), { status: 200 }) : new Response("no", { status: 500 }))));
  const { ensureConfig } = await import("./config");
  return (await ensureConfig()).rivianActions;
}

describe("rivian_actions in /api/config", () => {
  beforeEach(() => vi.resetModules());
  afterEach(() => vi.unstubAllGlobals());

  it("false (top level) turns the Rivian actions off", async () => {
    expect(await load({ rivian_actions: false })).toBe(false);
  });
  it("false under features turns them off too", async () => {
    expect(await load({ features: { rivian_actions: false } })).toBe(false);
  });
  it("true, or absent (an older server), keeps them", async () => {
    expect(await load({ rivian_actions: true })).toBe(true);
    expect(await load({ features: {} })).toBe(true);
    expect(await load({})).toBe(true);
    expect(await load(null)).toBe(true);
  });
  it("an unreadable config does not hide them", async () => {
    expect(await load(null, false)).toBe(true);
  });
});
