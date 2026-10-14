import { beforeEach, describe, expect, it, vi } from "vitest";

const cfg = { features: { landingEnabled: false, demoEnabled: false } };
const me = { status: 401 };
vi.mock("./config", () => ({ ensureConfig: async () => cfg, getConfig: () => cfg }));
vi.stubGlobal("fetch", async () => ({ status: me.status }));

import { resolveLanding } from "./landingGate";

describe("resolveLanding", () => {
  beforeEach(() => {
    cfg.features.landingEnabled = false;
    me.status = 401;
  });
  it("is off when the install hasn't enabled it, even signed out", async () => {
    expect(await resolveLanding()).toBe("off");
  });
  it("shows the landing only for signed-out visitors", async () => {
    cfg.features.landingEnabled = true;
    expect(await resolveLanding()).toBe("landing");
    me.status = 200;
    expect(await resolveLanding()).toBe("off");
    me.status = 404; // auth disabled
    expect(await resolveLanding()).toBe("off");
  });
});
