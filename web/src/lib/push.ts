import { backend, type PushPreferences } from "./api";

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

export function pushSupported(): boolean {
  return typeof window !== "undefined" && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

// Subscribes this device and registers it with the server. Asks the browser for permission only
// when it has not been granted, so call it from a tap. Resolves "denied" when the user says no.
export async function subscribeThisDevice(prefs: PushPreferences): Promise<"on" | "denied"> {
  if (Notification.permission !== "granted") {
    const p = await Notification.requestPermission();
    if (p !== "granted") return "denied";
  }
  const reg = await navigator.serviceWorker.ready;
  const { public_key } = await backend.pushVAPIDKey();
  if (!public_key) throw new Error("Server is missing a VAPID public key.");
  const s =
    (await reg.pushManager.getSubscription()) ??
    (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(public_key) }));
  const json = s.toJSON() as { endpoint?: string; keys?: { p256dh?: string; auth?: string } };
  if (!json.endpoint || !json.keys?.p256dh || !json.keys?.auth) throw new Error("Subscription is missing endpoint or keys.");
  await backend.pushSubscribe({
    endpoint: json.endpoint,
    keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
    preferences: prefs,
    user_agent: navigator.userAgent,
  });
  return "on";
}
