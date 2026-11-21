import { describe, expect, it } from "vitest";
import { isCharging, liveStatusLine } from "./LivePanel";

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

describe("isCharging (the Live panel, and so whether the picture is hidden)", () => {
  const plugged = "chrgr_sts_connected_charging";
  const st = (charger_state: string, charger_status: string, charger_power_kw = 0) => ({ charger_state, charger_status, charger_power_kw });
  it("counts an active charge, ready or power with the cable in", () => {
    expect(isCharging(st("charging_active", plugged))).toBe(true);
    expect(isCharging(st("charging_ready", plugged))).toBe(true);
    expect(isCharging(st("", plugged, 7))).toBe(true);
  });
  it("counts an active charge with no cable field (an R2 never reports it)", () => {
    expect(isCharging(st("charging_active", ""))).toBe(true);
    expect(isCharging(st("charging_ready", ""))).toBe(false);
  });
  it("does not count a finished session, an unplugged one or parked", () => {
    expect(isCharging(st("charging_complete", plugged, 7))).toBe(false);
    expect(isCharging(st("charging_active", "chrgr_sts_not_connected"))).toBe(false);
    expect(isCharging(st("charger_disconnected", ""))).toBe(false);
  });
});
