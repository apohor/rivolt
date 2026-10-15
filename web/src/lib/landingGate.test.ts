import { beforeEach, describe, expect, it, vi } from "vitest";

const cfg = { features: { landingEnabled: false, demoEnabled: false } };
const me: { status: number; body: unknown } = { status: 401, body: {} };
vi.mock("./config", () => ({ ensureConfig: async () => cfg, getConfig: () => cfg }));
vi.stubGlobal("fetch", async () => ({ status: me.status, json: async () => me.body }));

import { resolveLanding } from "./landingGate";

describe("resolveLanding", () => {
  beforeEach(() => {
    cfg.features.landingEnabled = false;
    me.status = 401;
    me.body = {};
  });
  it("is off when the install hasn't enabled it, even signed out", async () => {
    expect(await resolveLanding()).toBe("off");
  });
  it("shows the landing only for signed-out visitors", async () => {
    cfg.features.landingEnabled = true;
    expect(await resolveLanding()).toBe("landing");
    me.status = 200;
    me.body = { authenticated: true, user_id: "u1" };
    expect(await resolveLanding()).toBe("off");
    me.status = 404; // auth disabled
    expect(await resolveLanding()).toBe("off");
  });
  it("treats 200 {authenticated:false} (no session) as signed out", async () => {
    cfg.features.landingEnabled = true;
    me.status = 200;
    me.body = { authenticated: false };
    expect(await resolveLanding()).toBe("landing");
  });
  it("does not show the landing for an unreadable 200 body", async () => {
    cfg.features.landingEnabled = true;
    me.status = 200;
    me.body = null;
    expect(await resolveLanding()).toBe("off");
  });
});
