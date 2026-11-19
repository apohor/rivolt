import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Post = { events: { name: string; props?: Record<string, string> }[] };

// The entry URL is read when demoVisit loads, so each case sets the URL
// first and gets a fresh module graph.
async function setup(opts: { url: string; referrer?: string; dnt?: boolean; gpc?: boolean; events?: boolean }) {
  vi.resetModules();
  const posts: Post[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.startsWith("/api/config")) {
        return { ok: true, status: 200, json: async () => ({ features: { events_enabled: opts.events ?? true } }) } as Response;
      }
      if (url.startsWith("/api/events")) {
        posts.push(JSON.parse(String(init?.body)));
        return { ok: true, status: 204 } as Response;
      }
      return { ok: false, status: 404, json: async () => ({}) } as Response;
    }),
  );
  Object.defineProperty(navigator, "doNotTrack", { value: opts.dnt ? "1" : null, configurable: true });
  Object.defineProperty(navigator, "globalPrivacyControl", { value: opts.gpc === true, configurable: true });
  Object.defineProperty(document, "referrer", { value: opts.referrer ?? "", configurable: true });
  window.history.replaceState(null, "", opts.url);
  const mod = await import("./demoVisit");
  return { ...mod, posts };
}

const sent = (posts: Post[]) => posts.flatMap((p) => p.events);

describe("trackDemoVisit", () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("sends one demo_view per page load, with the sanitised source only", async () => {
    const t = await setup({
      url: "/demo/money?utm_source=Reddit&utm_medium=post&token=secret&x=1",
      referrer: "https://www.Reddit.com/r/Rivian/comments/abc?user=me",
    });
    t.trackDemoVisit(false);
    t.trackDemoVisit(false);
    await vi.advanceTimersByTimeAsync(1000);
    expect(sent(t.posts)).toEqual([{ name: "demo_view", props: { referrer_host: "www.reddit.com", utm_source: "reddit" } }]);
  });

  it("drops a utm_source that is not a token", async () => {
    const t = await setup({ url: "/demo?utm_source=" + encodeURIComponent("https://evil.test/?a=b") });
    t.trackDemoVisit(false);
    await vi.advanceTimersByTimeAsync(1000);
    expect(sent(t.posts)).toEqual([{ name: "demo_view", props: {} }]);
  });

  it("counts the standalone demo's root entry, read before the redirect to /demo", async () => {
    const t = await setup({ url: "/?utm_source=x" });
    window.history.replaceState(null, "", "/demo"); // the redirect drops the query
    t.trackDemoVisit(true);
    await vi.advanceTimersByTimeAsync(1000);
    expect(sent(t.posts)).toEqual([{ name: "demo_view", props: { utm_source: "x" } }]);
  });

  it("does not count entries that are not the demo", async () => {
    const root = await setup({ url: "/?utm_source=x" });
    root.trackDemoVisit(false); // rivolt.dev landing page: landing_view's job
    const privacy = await setup({ url: "/privacy" });
    privacy.trackDemoVisit(true);
    await vi.advanceTimersByTimeAsync(1000);
    expect(sent(root.posts)).toEqual([]);
    expect(sent(privacy.posts)).toEqual([]);
  });

  it("sends nothing under Do-Not-Track or Global Privacy Control", async () => {
    const dnt = await setup({ url: "/demo?utm_source=x", dnt: true });
    dnt.trackDemoVisit(false);
    await vi.advanceTimersByTimeAsync(1000);
    const gpc = await setup({ url: "/demo?utm_source=x", gpc: true });
    gpc.trackDemoVisit(false);
    await vi.advanceTimersByTimeAsync(1000);
    expect(dnt.posts).toHaveLength(0);
    expect(gpc.posts).toHaveLength(0);
  });

  it("sends nothing while the server has events off", async () => {
    const t = await setup({ url: "/demo", events: false });
    t.trackDemoVisit(false);
    await vi.advanceTimersByTimeAsync(1000);
    expect(t.posts).toHaveLength(0);
  });
});
