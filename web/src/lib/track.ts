// First-party product events (docs/product/instrumentation.md). Counts
// and enums only: no PII, no coordinates, no amounts. Off unless the
// server says RIVOLT_EVENTS (features.events_enabled); also silent under
// Do-Not-Track / Global Privacy Control. The visitor id is a random
// per-browser id kept in localStorage; if storage is blocked, nothing is
// sent. Failures are swallowed: analytics never affects the app.

import { ensureConfig } from "./config";

const ANON_KEY = "rivolt.anon_id";
const MAX_BATCH = 20;
const FLUSH_MS = 400;

type Ev = { name: string; props?: Record<string, string | number | boolean> };

let queue: Ev[] = [];
let timer: ReturnType<typeof setTimeout> | null = null;

function dntOn(): boolean {
  const n = typeof navigator !== "undefined" ? (navigator as Navigator & { globalPrivacyControl?: boolean }) : null;
  if (!n) return false;
  return n.doNotTrack === "1" || n.globalPrivacyControl === true;
}

export function anonID(): string | null {
  try {
    let id = window.localStorage.getItem(ANON_KEY);
    if (!id || !/^[A-Za-z0-9_-]{16,64}$/.test(id)) {
      const bytes = new Uint8Array(18);
      crypto.getRandomValues(bytes);
      id = btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
      window.localStorage.setItem(ANON_KEY, id);
    }
    return id;
  } catch {
    return null;
  }
}

async function flush() {
  timer = null;
  const batch = queue.splice(0, MAX_BATCH);
  if (queue.length > 0) timer = setTimeout(flush, FLUSH_MS);
  if (batch.length === 0) return;
  try {
    const cfg = await ensureConfig();
    if (!cfg.features.eventsEnabled || dntOn()) return;
    const id = anonID();
    if (!id) return;
    await fetch("/api/events", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      keepalive: true,
      body: JSON.stringify({ anon_id: id, events: batch }),
    });
  } catch {
    /* never surface analytics failures */
  }
}

// track queues an event; it's a no-op (nothing queued) under DNT/GPC.
export function track(name: string, props?: Ev["props"]) {
  if (dntOn()) return;
  queue.push({ name, props });
  if (!timer) timer = setTimeout(flush, FLUSH_MS);
}

// Test-only.
export function _resetTrackForTests() {
  queue = [];
  if (timer) clearTimeout(timer);
  timer = null;
}
