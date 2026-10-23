import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { adminSitePath } from "../App";

describe("adminSitePath", () => {
  it("serves the admin page, sign-in and the OIDC hand-off", () => {
    for (const p of ["/admin", "/admin/", "/admin/users", "/login", "/auth/hydra/login"]) expect(adminSitePath(p), p).toBe(true);
  });
  it("sends everything else to /admin", () => {
    for (const p of ["/", "/drives", "/drives/1", "/settings", "/live", "/money", "/trips/plan", "/onboarding", "/signup", "/administrator", "/loginx"]) expect(adminSitePath(p), p).toBe(false);
  });
});

// config.ts caches /api/config at module level, so each case gets a fresh module graph.
async function load(features: Record<string, unknown> | null) {
  vi.resetModules();
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(features === null ? {} : { features }), { status: 200 })));
  const config = await import("./config");
  const cfg = await config.ensureConfig();
  return { cfg };
}

describe("features.admin_only", () => {
  beforeEach(() => vi.resetModules());
  afterEach(() => vi.unstubAllGlobals());

  it("true: the admin site", async () => {
    const { cfg } = await load({ admin_only: true });
    expect(cfg.features.adminOnly).toBe(true);
  });
  it("absent or false: not the admin site", async () => {
    for (const f of [{}, { admin_only: false }, null]) {
      const { cfg } = await load(f as Record<string, unknown> | null);
      expect(cfg.features.adminOnly).toBe(false);
    }
  });
});
