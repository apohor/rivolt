import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const pushSubscribe = vi.fn(async () => ({ ok: true, preferences: {} }));
vi.mock("./api", () => ({
  backend: { pushVAPIDKey: async () => ({ public_key: "AAAA" }), pushSubscribe: (b: unknown) => pushSubscribe(b) },
}));
import { subscribeThisDevice } from "./push";

const prefs = { on_charging_done: false, on_plug_in_reminder: false, on_anomaly: false, on_software_update: true };
const sub = { endpoint: "https://push.example/1", toJSON: () => ({ endpoint: "https://push.example/1", keys: { p256dh: "p", auth: "a" } }) };

function fake(existing: unknown) {
  const subscribe = vi.fn(async () => sub);
  vi.stubGlobal("Notification", { permission: "granted", requestPermission: vi.fn() });
  vi.stubGlobal("navigator", { userAgent: "test", serviceWorker: { ready: Promise.resolve({ pushManager: { getSubscription: async () => existing, subscribe } }) } });
  return subscribe;
}

describe("subscribeThisDevice", () => {
  beforeEach(() => pushSubscribe.mockClear());
  afterEach(() => vi.unstubAllGlobals());

  it("registers a new device with the given preferences", async () => {
    const subscribe = fake(null);
    expect(await subscribeThisDevice(prefs, { leaveExisting: true })).toBe("on");
    expect(subscribe).toHaveBeenCalledOnce();
    expect(pushSubscribe).toHaveBeenCalledWith(expect.objectContaining({ preferences: prefs }));
  });

  it("leaves an already-subscribed device's preferences alone", async () => {
    const subscribe = fake(sub);
    expect(await subscribeThisDevice(prefs, { leaveExisting: true })).toBe("on");
    expect(subscribe).not.toHaveBeenCalled();
    expect(pushSubscribe).not.toHaveBeenCalled();
  });

  it("without leaveExisting, re-posts for an existing subscription (Settings flow)", async () => {
    fake(sub);
    await subscribeThisDevice(prefs);
    expect(pushSubscribe).toHaveBeenCalledOnce();
  });
});
