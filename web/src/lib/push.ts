import { ApiError, backend, type PushPreferences } from "./api";

// urlBase64ToUint8Array converts the URL-safe base64 VAPID public key
// the server returns into the raw byte array PushManager.subscribe
// requires for applicationServerKey.
export function urlBase64ToUint8Array(base64String: string): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(base64);
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// What the person is told when the server refuses their browser's push service (an unfamiliar host). Nothing was saved.
export function unsupportedPushMessage(host?: string): string {
  return `This browser's push service${host ? ` (${host})` : ""} isn't supported yet, so notifications can't be turned on here. Nothing was saved.`;
}

// The server refused this browser's push service. The message is for the person; the cause is the server's answer.
export class PushRefusedError extends Error {
  host?: string;
  cause: unknown;
  constructor(host: string | undefined, cause: unknown) {
    super(unsupportedPushMessage(host));
    this.name = "PushRefusedError";
    this.host = host;
    this.cause = cause;
  }
}

// Registers a browser push subscription with the server, and leaves the browser and the server telling the same story. If the server
// refuses (or the call fails) after this call created the subscription, the browser subscription is removed again, so a refresh does
// not show notifications as on when the server has nothing; one that already existed is removed too when the server says the push
// service itself is unsupported (it can never work). A refusal by an unfamiliar push service comes back as an Error with a plain
// message; anything else is rethrown as it was. Returns the subscription and the preferences the server stored.
export async function registerPushSubscription(
  reg: ServiceWorkerRegistration,
  publicKey: string,
  prefs: PushPreferences,
): Promise<{ sub: PushSubscription; preferences: PushPreferences }> {
  const existing = await reg.pushManager.getSubscription();
  const s = existing ?? (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(publicKey) }));
  try {
    const json = s.toJSON() as { endpoint?: string; keys?: { p256dh?: string; auth?: string } };
    if (!json.endpoint || !json.keys?.p256dh || !json.keys?.auth) throw new Error("Subscription is missing endpoint or keys.");
    const res = await backend.pushSubscribe({
      endpoint: json.endpoint,
      keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
      preferences: prefs,
      user_agent: navigator.userAgent,
    });
    return { sub: s, preferences: res.preferences };
  } catch (e) {
    const unsupported = e instanceof ApiError && e.status === 400 && (e.body as { class?: string } | null)?.class === "unsupported_push_service";
    if (!existing || unsupported) await s.unsubscribe().catch(() => false);
    if (unsupported) throw new PushRefusedError((e.body as { host?: string }).host, e);
    throw e;
  }
}

export function pushSupported(): boolean {
  return typeof window !== "undefined" && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

// Subscribes this device and registers it with the server. Asks the browser for permission only
// when it has not been granted, so call it from a tap. Resolves "denied" when the user says no.
// The server replaces a device's preferences with whatever is posted, and the client cannot read
// them back, so a device that is already subscribed is left alone when `leaveExisting` is set
// (its kinds were chosen in Settings and must not be reset).
export async function subscribeThisDevice(prefs: PushPreferences, opts?: { leaveExisting?: boolean }): Promise<"on" | "denied"> {
  if (Notification.permission !== "granted") {
    const p = await Notification.requestPermission();
    if (p !== "granted") return "denied";
  }
  const reg = await navigator.serviceWorker.ready;
  const { public_key } = await backend.pushVAPIDKey();
  if (!public_key) throw new Error("Server is missing a VAPID public key.");
  if (opts?.leaveExisting && (await reg.pushManager.getSubscription())) return "on";
  await registerPushSubscription(reg, public_key, prefs);
  return "on";
}
