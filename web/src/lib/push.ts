// Browser-side Web Push helpers.
//
// The Caffeine backend holds the VAPID keypair and a list of subscriptions.
// This file is the client-side counterpart: it asks the browser for
// permission, subscribes via the service worker's PushManager, and hands
// the resulting subscription to the backend so it can be targeted later.
//
// Intentionally opinionated defaults:
//   - Only attempts anything on secure contexts (HTTPS or localhost).
//   - Uses the browser's OS-managed permission prompt; never pops a custom
//     in-page modal that would just duplicate it.
//   - Assumes a single VAPID applicationServerKey per origin (the one
//     fetched from /api/push/vapid-public-key).

import { api } from "./api";

export type PushPreferences = {
  on_shot_finished: boolean;
  on_analysis_ready: boolean;
};

export type PushStatus = {
  // The server is wired up and can sign outbound pushes.
  enabled: boolean;
  // Count of subscriptions recorded server-side, across all devices.
  subscription_count: number;
};

export type PushState = {
  // Feature-level: the SW is registered and the browser exposes
  // the PushManager. False on HTTP, old browsers, iOS pre-16.4, etc.
  supported: boolean;
  // Permission state as reported by Notification.permission.
  permission: NotificationPermission | "unsupported";
  // Endpoint of the browser's current subscription, or null.
  endpoint: string | null;
  // What the backend tells us about its side of the pipe.
  server: PushStatus | null;
};

/** True when the runtime can host a real push subscription. */
export function pushSupported(): boolean {
  if (typeof window === "undefined") return false;
  if (!("serviceWorker" in navigator)) return false;
  if (!("PushManager" in window)) return false;
  // iOS refuses push outside installed PWAs, and the browser also
  // refuses on insecure origins. Both conditions are caught by the
  // actual subscribe() call failing, so we don't need to replicate
  // that logic here — but we do need secure context.
  return Boolean(window.isSecureContext) || location.hostname === "localhost";
}

/** Snapshot of everything the Settings UI needs to render. */
export async function readPushState(): Promise<PushState> {
  const base: PushState = {
    supported: pushSupported(),
    permission: "unsupported",
    endpoint: null,
    server: null,
  };
  if (!base.supported) return base;

  base.permission = (typeof Notification !== "undefined"
    ? Notification.permission
    : "default") as NotificationPermission;

  try {
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.getSubscription();
    base.endpoint = sub?.endpoint ?? null;
  } catch {
    /* ignore — leave endpoint null */
  }

  try {
    base.server = await api.get<PushStatus>("/api/push/status");
  } catch {
    /* backend push disabled: show 'unavailable' in UI */
  }

  return base;
}

/**
 * Request permission (if needed), subscribe via PushManager, and POST the
 * subscription to the backend. Returns the resulting PushState.
 *
 * Throws with a human-readable message if anything fails, so the UI can
 * show it in an ErrorBox.
 */
export async function enablePush(prefs?: PushPreferences): Promise<PushState> {
  if (!pushSupported()) {
    throw new Error("This browser does not support push notifications.");
  }

  if (Notification.permission === "denied") {
    throw new Error(
      "Notification permission is blocked. Re-enable it in your browser settings.",
    );
  }
  if (Notification.permission !== "granted") {
    const result = await Notification.requestPermission();
    if (result !== "granted") {
      throw new Error("Permission denied.");
    }
  }

  // Fetch the server's VAPID public key. Without this pushManager will
  // refuse to subscribe.
  const { public_key } = await api.get<{ public_key: string }>(
    "/api/push/vapid-public-key",
  );
  if (!public_key) {
    throw new Error("Server push is disabled (no VAPID key).");
  }

  const reg = await navigator.serviceWorker.ready;

  // Reuse an existing browser subscription if one exists; pushManager
  // refuses to subscribe again otherwise. If the applicationServerKey
  // doesn't match (unlikely unless the server rotated keys) we drop it
  // and subscribe fresh.
  let sub = await reg.pushManager.getSubscription();
  if (sub) {
    // Cheap correctness check: if the stored endpoint still round-trips
    // via the same key we can keep it. Otherwise unsubscribe + redo.
    const options = sub.options;
    const sameKey =
      options?.applicationServerKey &&
      base64ToHex(options.applicationServerKey as ArrayBuffer) ===
        base64urlToHex(public_key);
    if (!sameKey) {
      await sub.unsubscribe().catch(() => {});
      sub = null;
    }
  }
  if (!sub) {
    sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(public_key),
    });
  }

  await api.post("/api/push/subscribe", {
    ...sub.toJSON(),
    preferences: prefs ?? { on_shot_finished: true, on_analysis_ready: true },
    user_agent: navigator.userAgent,
  });

  return readPushState();
}

/** Disable push: unsubscribe in the browser and delete the server row. */
export async function disablePush(): Promise<PushState> {
  if (!pushSupported()) return readPushState();
  const reg = await navigator.serviceWorker.ready;
  const sub = await reg.pushManager.getSubscription();
  if (sub) {
    await api
      .post("/api/push/unsubscribe", { endpoint: sub.endpoint })
      .catch(() => {});
    await sub.unsubscribe().catch(() => {});
  }
  return readPushState();
}

/** Send a test push to the current subscription. */
export async function sendTestPush(): Promise<void> {
  if (!pushSupported()) throw new Error("Push not supported.");
  const reg = await navigator.serviceWorker.ready;
  const sub = await reg.pushManager.getSubscription();
  if (!sub) throw new Error("No active subscription.");
  await api.post("/api/push/test", { endpoint: sub.endpoint });
}

// VAPID keys travel as base64url-encoded raw 65-byte P-256 points.
// PushManager.subscribe wants them as a BufferSource whose buffer is an
// ArrayBuffer (not SharedArrayBuffer), so we allocate an ArrayBuffer
// explicitly to satisfy TS 5.7's stricter lib.dom typings.
function urlBase64ToUint8Array(base64url: string): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (base64url.length % 4)) % 4);
  const base64 = (base64url + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(base64);
  const buf = new ArrayBuffer(raw.length);
  const out = new Uint8Array(buf);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out as Uint8Array<ArrayBuffer>;
}

// Helpers for the key-equality check above. Kept tiny on purpose.
function base64ToHex(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let h = "";
  for (let i = 0; i < bytes.length; i++)
    h += bytes[i].toString(16).padStart(2, "0");
  return h;
}
function base64urlToHex(s: string): string {
  return base64ToHex(urlBase64ToUint8Array(s).buffer);
}
