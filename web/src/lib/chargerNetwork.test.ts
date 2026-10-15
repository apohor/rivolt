import { describe, expect, it } from "vitest";
import { choosePin, displayNetwork, networkKey } from "./chargerNetwork";

describe("networkKey", () => {
  it("normalises the networks we see", () => {
    expect(networkKey("Tesla Supercharger")).toBe("tesla");
    expect(networkKey("Rivian Adventure Network")).toBe("rivian");
    expect(networkKey("RAN")).toBe("rivian");
    expect(networkKey("Electrify America")).toBe("electrify_america");
    expect(networkKey("EA")).toBe("electrify_america");
    expect(networkKey(undefined, "")).toBeNull();
    expect(networkKey("Some Local Charger")).toBeNull();
  });
});

describe("choosePin", () => {
  const tesla = { name: "Tesla Supercharger", operator: "Tesla, Inc.", brand: "Tesla" };

  it("keeps the snap when the snapped charger agrees with the receipt", () => {
    const p = choosePin({ name: "Electrify America", network: "Electrify America" }, "Electrify America", true);
    expect(p).toEqual({ snap: true, label: "Electrify America" });
  });
  it("mismatch: labels the charge's own location with the known network (RAN beside a Supercharger)", () => {
    const p = choosePin(tesla, "Rivian Adventure Network", true);
    expect(p).toEqual({ snap: false, label: "Rivian Adventure Network" });
  });
  it("mismatch: EA beside a Supercharger says Electrify America, never Tesla", () => {
    const p = choosePin(tesla, "Electrify America", true);
    expect(p).toEqual({ snap: false, label: "Electrify America" });
  });
  it("accepts the ledger's short forms", () => {
    expect(choosePin(tesla, "RAN", true)).toEqual({ snap: false, label: "Rivian Adventure Network" });
    expect(choosePin(tesla, "Tesla", true)).toEqual({ snap: true, label: "Tesla Supercharger" });
  });
  it("unknown network: unchanged behaviour, snap with the OSM name", () => {
    expect(choosePin(tesla, null, true)).toEqual({ snap: true, label: "Tesla Supercharger" });
    expect(choosePin({ name: "Joe's Chargers" }, "unknown", true)).toEqual({ snap: true, label: "Joe's Chargers" });
  });
  it("unknown network and no POI: a generic label, no brand; 'DC fast charger' only for DC", () => {
    expect(choosePin(null, null, true)).toEqual({ snap: false, label: "DC fast charger" });
    expect(choosePin(null, null, false)).toEqual({ snap: false, label: "Charging station" });
    expect(choosePin(null, null)).toEqual({ snap: false, label: "Charging station" });
  });
  it("known network but no POI: the known network at the charge's location", () => {
    expect(choosePin(null, "EVgo", true)).toEqual({ snap: false, label: "EVgo" });
  });
  it("an unverifiable node (no network info) isn't trusted against a known network", () => {
    expect(choosePin({ name: "Charging station" }, "Rivian Adventure Network", true)).toEqual({
      snap: false,
      label: "Rivian Adventure Network",
    });
  });
  it("shows an unrecognised ledger network verbatim", () => {
    expect(displayNetwork("  Blink  ")).toBe("Blink");
    expect(choosePin(tesla, "Blink", true)).toEqual({ snap: false, label: "Blink" });
  });
  it("AC session beside a Supercharger: no snap, generic label", () => {
    expect(choosePin(tesla, null, false)).toEqual({ snap: false, label: "Charging station" });
    expect(choosePin(tesla, "unknown", false)).toEqual({ snap: false, label: "Charging station" });
  });
  it("AC session with a known network keeps that network's name, never the DC node's", () => {
    expect(choosePin(tesla, "ChargePoint", false)).toEqual({ snap: false, label: "ChargePoint" });
  });
  it("DC session with unknown network beside a Supercharger: snap stays", () => {
    expect(choosePin(tesla, null, true)).toEqual({ snap: true, label: "Tesla Supercharger" });
  });
  it("AC session snaps to an AC or untyped node", () => {
    expect(choosePin({ name: "Hotel EV parking", operator: "ChargePoint", maxPowerKW: 7 }, null, false)).toEqual({
      snap: true,
      label: "Hotel EV parking",
    });
    expect(choosePin({ name: "Hotel" }, null, false)).toEqual({ snap: true, label: "Hotel" });
  });
});
