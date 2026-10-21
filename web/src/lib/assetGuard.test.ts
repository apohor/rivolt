import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// public/asset-guard.js runs before the app (a blocking classic script), so it
// is tested as that file, against fakes for what it touches.
const code = readFileSync("public/asset-guard.js", "utf8");

type Handler = (e: unknown) => void;

function page(storage: Map<string, string> | null, now: { t: number }) {
  const handlers: Record<string, Handler[]> = {};
  const timers: (() => void)[] = [];
  let reloads = 0;
  const win = {
    addEventListener: (type: string, h: Handler) => {
      (handlers[type] ||= []).push(h);
    },
  };
  const sessionStorage = {
    getItem: (k: string) => {
      if (!storage) throw new Error("blocked");
      return storage.get(k) ?? null;
    },
    setItem: (k: string, v: string) => {
      if (!storage) throw new Error("blocked");
      storage.set(k, v);
    },
  };
  const location = { reload: () => reloads++ };
  const setTimeout = (fn: () => void) => timers.push(fn);
  const date = { now: () => now.t };
  new Function("window", "sessionStorage", "location", "setTimeout", "Date", code)(
    win,
    sessionStorage,
    location,
    setTimeout,
    date,
  );
  return {
    error: (target: unknown) => handlers.error.forEach((h) => h({ target })),
    preloadError: () => {
      let prevented = false;
      handlers["vite:preloadError"].forEach((h) => h({ preventDefault: () => (prevented = true) }));
      return prevented;
    },
    flush: () => timers.splice(0).forEach((fn) => fn()),
    reloads: () => reloads,
  };
}

const script = (src: string) => ({ tagName: "SCRIPT", src });

describe("asset-guard.js", () => {
  it("reloads once when an /assets/ script or stylesheet fails", () => {
    const now = { t: 0 };
    const p = page(new Map(), now);
    p.error(script("https://rivolt.dev/assets/index-NEW.js"));
    p.error(script("https://rivolt.dev/assets/vendor-NEW.js"));
    expect(p.reloads()).toBe(0);
    p.flush();
    expect(p.reloads()).toBe(1);

    const q = page(new Map(), now);
    q.error({ tagName: "LINK", rel: "stylesheet", href: "https://rivolt.dev/assets/index-NEW.css" });
    q.flush();
    expect(q.reloads()).toBe(1);
  });

  it("ignores everything that isn't an /assets/ file", () => {
    const p = page(new Map(), { t: 0 });
    p.error(script("https://rivolt.dev/theme-init.js"));
    p.error({ tagName: "IMG", src: "https://rivolt.dev/assets/x.png" });
    p.error(undefined);
    p.flush();
    expect(p.reloads()).toBe(0);
  });

  it("stops after two reloads in two minutes, then lets the error through", () => {
    const storage = new Map<string, string>();
    const now = { t: 1_000 };
    for (let i = 0; i < 2; i++) {
      const p = page(storage, now);
      expect(p.preloadError()).toBe(true);
      p.flush();
      expect(p.reloads()).toBe(1);
      now.t += 10_000;
    }
    const third = page(storage, now);
    expect(third.preloadError()).toBe(false);
    third.flush();
    expect(third.reloads()).toBe(0);

    now.t += 2 * 60 * 1000;
    const later = page(storage, now);
    expect(later.preloadError()).toBe(true);
  });

  it("never reloads without storage", () => {
    const p = page(null, { t: 0 });
    expect(p.preloadError()).toBe(false);
    p.error(script("https://rivolt.dev/assets/index-NEW.js"));
    p.flush();
    expect(p.reloads()).toBe(0);
  });
});
