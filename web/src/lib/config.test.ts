import { afterEach, describe, expect, it, vi } from "vitest";

// features.demo_only / app_url drive the standalone demo deployment.
async function loadWith(body: unknown) {
  vi.resetModules();
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({ ok: true, json: async () => body }),
  );
  const mod = await import("./config");
  return mod.ensureConfig();
}

describe("runtime config: demo-only deployment", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("reads demo_only and a trimmed app_url", async () => {
    const c = await loadWith({ features: { demo_only: true, app_url: "https://rivolt.dev/" } });
    expect(c.features.demoOnly).toBe(true);
    expect(c.features.appUrl).toBe("https://rivolt.dev");
  });

  it("defaults to a normal deployment when the server doesn't send them", async () => {
    const c = await loadWith({ features: {} });
    expect(c.features.demoOnly).toBe(false);
    expect(c.features.appUrl).toBe("");
  });
});
