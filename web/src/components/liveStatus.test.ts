import { describe, expect, it } from "vitest";
import { liveStatusLine } from "./LivePanel";

describe("liveStatusLine", () => {
  it("says what the truck shows while power flows, with no waiting", () => {
    expect(liveStatusLine("charging_active", 7.6)).toBe("Charging · 7.6 kW");
    expect(liveStatusLine("charger_connected", 150)).toBe("Charging · 150.0 kW");
  });
  it("waits for a first frame only when there is nothing yet", () => {
    expect(liveStatusLine("charging_active", 0)).toBe("active · waiting for first frame…");
    expect(liveStatusLine("charger_connected", 0)).toBe("connected · waiting for first frame…");
  });
});
