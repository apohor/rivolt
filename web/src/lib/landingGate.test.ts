import { beforeEach, describe, expect, it, vi } from "vitest";

const cfg = { features: { landingEnabled: false, demoEnabled: false } };
const me = { value: null as unknown };
vi.mock("./config", () => ({ ensureConfig: async () => cfg, getConfig: () => cfg }));
vi.mock("./api", () => ({ backend: { whoami: async () => me.value } }));

import { resolveLanding } from "./landingGate";

describe("resolveLanding", () => {
  beforeEach(() => {
    cfg.features.landingEnabled = false;
    me.value = null;
  });
  it("is off when the install hasn't enabled it, even signed out", async () => {
    expect(await resolveLanding()).toBe("off");
  });
  it("shows the landing only for signed-out visitors", async () => {
    cfg.features.landingEnabled = true;
    expect(await resolveLanding()).toBe("landing");
    me.value = { user_id: "u", username: "a" };
    expect(await resolveLanding()).toBe("off");
  });
});
