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

describe("runtime config: demo radar", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    window.history.pushState({}, "", "/");
  });
  const tiles = {
    radar_url: "/api/maps/radar/{z}/{x}/{y}.png",
    radar_frame_url: "/api/maps/radar/frame",
    radar_minzoom: 3,
    radar_maxzoom: 9,
    radar_demo_url: "/api/demo/radar/{z}/{x}/{y}.png",
    radar_demo_frame_url: "/api/demo/radar/frame",
    radar_demo_minzoom: 4,
    radar_demo_maxzoom: 7,
  };

  it("uses the demo's own radar routes and zooms in the signed-out demo, the signed-in ones elsewhere", async () => {
    await loadWith({ tiles });
    const mod = await import("./config");
    expect(mod.tilesRadar()).toEqual({ url: tiles.radar_url, frameUrl: tiles.radar_frame_url, minZoom: 3, maxZoom: 9 });
    window.history.pushState({}, "", "/demo/trips/plan");
    expect(mod.tilesRadar()).toEqual({ url: tiles.radar_demo_url, frameUrl: tiles.radar_demo_frame_url, minZoom: 4, maxZoom: 7 });
  });

  it("has no radar in the demo when the server does not advertise the demo routes", async () => {
    await loadWith({ tiles: { radar_url: tiles.radar_url, radar_frame_url: tiles.radar_frame_url } });
    const mod = await import("./config");
    window.history.pushState({}, "", "/demo/trips/plan");
    expect(mod.tilesRadar().url).toBe("");
  });
});
