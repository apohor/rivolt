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
  const ui = await import("./uiNext");
  const cfg = await config.ensureConfig();
  return { cfg, ui };
}

describe("features.admin_only", () => {
  beforeEach(() => vi.resetModules());
  afterEach(() => vi.unstubAllGlobals());

  it("true: the admin site, with the new look forced on (no ledger call needed)", async () => {
    const { cfg, ui } = await load({ admin_only: true });
    expect(cfg.features.adminOnly).toBe(true);
    expect(ui.uiNextEnabled("admin.rivolt.dev")).toBe(true);
  });
  it("absent or false: today's behaviour, the new look left to the server flag", async () => {
    for (const f of [{}, { admin_only: false }, null]) {
      const { cfg, ui } = await load(f as Record<string, unknown> | null);
      expect(cfg.features.adminOnly).toBe(false);
      expect(ui.uiNextEnabled("rivolt.dev")).toBe(false);
    }
  });
});
