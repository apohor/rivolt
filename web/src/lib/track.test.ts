import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// config.ts caches /api/config at module level and track.ts keeps a
// queue, so each case gets a fresh module graph.
async function setup(opts: { events: boolean; dnt?: boolean }) {
  vi.resetModules();
  const posts: { url: string; body: { anon_id: string; events: { name: string; props?: object }[] } }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.startsWith("/api/config")) {
        return { ok: true, status: 200, json: async () => ({ features: { events_enabled: opts.events } }) } as Response;
      }
      if (url.startsWith("/api/events")) {
        posts.push({ url, body: JSON.parse(String(init?.body)) });
        return { ok: true, status: 204 } as Response;
      }
      return { ok: false, status: 404, json: async () => ({}) } as Response;
    }),
  );
  Object.defineProperty(navigator, "doNotTrack", { value: opts.dnt ? "1" : null, configurable: true });
  const mod = await import("./track");
  return { ...mod, posts };
}

describe("track", () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("sends nothing while events are off server-side", async () => {
    const t = await setup({ events: false });
    t.track("demo_view", { tab: "now" });
    await vi.advanceTimersByTimeAsync(1000);
    expect(t.posts).toHaveLength(0);
  });

  it("batches events with a random anon id when on", async () => {
    const t = await setup({ events: true });
    t.track("demo_view", { tab: "now" });
    t.track("demo_tab", { tab: "money" });
    await vi.advanceTimersByTimeAsync(1000);
    expect(t.posts).toHaveLength(1);
    const b = t.posts[0].body;
    expect(b.anon_id).toMatch(/^[A-Za-z0-9_-]{16,64}$/);
    expect(b.events.map((e) => e.name)).toEqual(["demo_view", "demo_tab"]);
    // The same browser keeps the same id.
    expect(window.localStorage.getItem("rivolt.anon_id")).toBe(b.anon_id);
  });

  it("honours Do-Not-Track: nothing queued, nothing sent", async () => {
    const t = await setup({ events: true, dnt: true });
    t.track("demo_view", { tab: "now" });
    await vi.advanceTimersByTimeAsync(1000);
    expect(t.posts).toHaveLength(0);
  });

  it("sends nothing when storage is blocked (no id to attach)", async () => {
    const t = await setup({ events: true });
    const spy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    t.track("demo_view", { tab: "now" });
    await vi.advanceTimersByTimeAsync(1000);
    spy.mockRestore();
    expect(t.posts).toHaveLength(0);
  });
});
