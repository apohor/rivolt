import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const pushSubscribe = vi.fn(async (_b: unknown): Promise<{ ok: boolean; preferences: object }> => ({ ok: true, preferences: {} }));
vi.mock("./api", () => {
  class ApiError extends Error {
    status: number;
    body: unknown;
    constructor(status: number, body: unknown, msg?: string) {
      super(msg ?? "error");
      this.status = status;
      this.body = body;
    }
  }
  return {
    ApiError,
    backend: { pushVAPIDKey: async () => ({ public_key: "AAAA" }), pushSubscribe: (b: unknown) => pushSubscribe(b) },
  };
});
import { ApiError } from "./api";
import { subscribeThisDevice, unsupportedPushMessage } from "./push";

const prefs = { on_charging_done: false, on_plug_in_reminder: false, on_anomaly: false, on_software_update: true };
const sub = { endpoint: "https://push.example/1", toJSON: () => ({ endpoint: "https://push.example/1", keys: { p256dh: "p", auth: "a" } }) };

const unsubscribe = vi.fn(async () => true);
function fake(existing: unknown) {
  const subscribe = vi.fn(async () => ({ ...sub, unsubscribe }));
  vi.stubGlobal("Notification", { permission: "granted", requestPermission: vi.fn() });
  vi.stubGlobal("navigator", { userAgent: "test", serviceWorker: { ready: Promise.resolve({ pushManager: { getSubscription: async () => existing, subscribe } }) } });
  return subscribe;
}

describe("subscribeThisDevice", () => {
  beforeEach(() => {
    pushSubscribe.mockClear();
    unsubscribe.mockClear();
  });
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

  // issue #1201: the server refused an unfamiliar push service, and a refresh then showed notifications as on.
  const unsupported = () => new ApiError(400, { error: "unsupported push service", class: "unsupported_push_service", host: "jmt99.google.com" });

  it("removes the browser subscription it just made when the server refuses the push service, and says so plainly", async () => {
    fake(null);
    pushSubscribe.mockRejectedValueOnce(unsupported());
    await expect(subscribeThisDevice(prefs)).rejects.toThrow(unsupportedPushMessage("jmt99.google.com"));
    expect(unsubscribe).toHaveBeenCalledOnce();
  });

  it("removes a subscription that already existed when the server says its push service can never work", async () => {
    fake({ ...sub, unsubscribe });
    pushSubscribe.mockRejectedValueOnce(unsupported());
    await expect(subscribeThisDevice(prefs)).rejects.toThrow("isn't supported yet");
    expect(unsubscribe).toHaveBeenCalledOnce();
  });

  it("removes a new subscription after any failed registration, but keeps an existing one on a passing failure", async () => {
    fake(null);
    pushSubscribe.mockRejectedValueOnce(new ApiError(500, null, "boom"));
    await expect(subscribeThisDevice(prefs)).rejects.toThrow("boom");
    expect(unsubscribe).toHaveBeenCalledOnce();

    unsubscribe.mockClear();
    fake({ ...sub, unsubscribe });
    pushSubscribe.mockRejectedValueOnce(new ApiError(500, null, "boom"));
    await expect(subscribeThisDevice(prefs)).rejects.toThrow("boom");
    expect(unsubscribe).not.toHaveBeenCalled();
  });

  it("names no host when the server did not send one", () => {
    expect(unsupportedPushMessage()).not.toContain("()");
    expect(unsupportedPushMessage("x.example")).toContain("(x.example)");
  });
});
