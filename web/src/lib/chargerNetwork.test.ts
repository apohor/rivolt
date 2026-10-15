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
    const p = choosePin({ name: "Electrify America", network: "Electrify America" }, "Electrify America");
    expect(p).toEqual({ snap: true, label: "Electrify America" });
  });
  it("mismatch: labels the charge's own location with the known network (RAN beside a Supercharger)", () => {
    const p = choosePin(tesla, "Rivian Adventure Network");
    expect(p).toEqual({ snap: false, label: "Rivian Adventure Network" });
  });
  it("mismatch: EA beside a Supercharger says Electrify America, never Tesla", () => {
    const p = choosePin(tesla, "Electrify America");
    expect(p).toEqual({ snap: false, label: "Electrify America" });
  });
  it("accepts the ledger's short forms", () => {
    expect(choosePin(tesla, "RAN")).toEqual({ snap: false, label: "Rivian Adventure Network" });
    expect(choosePin(tesla, "Tesla")).toEqual({ snap: true, label: "Tesla Supercharger" });
  });
  it("unknown network: unchanged behaviour, snap with the OSM name", () => {
    expect(choosePin(tesla, null)).toEqual({ snap: true, label: "Tesla Supercharger" });
    expect(choosePin({ name: "Joe's Chargers" }, "unknown")).toEqual({ snap: true, label: "Joe's Chargers" });
  });
  it("unknown network and no POI: a generic label, no brand; 'DC fast charger' only for DC", () => {
    expect(choosePin(null, null, true)).toEqual({ snap: false, label: "DC fast charger" });
    expect(choosePin(null, null, false)).toEqual({ snap: false, label: "Charging station" });
    expect(choosePin(null, null)).toEqual({ snap: false, label: "Charging station" });
  });
  it("known network but no POI: the known network at the charge's location", () => {
    expect(choosePin(null, "EVgo")).toEqual({ snap: false, label: "EVgo" });
  });
  it("an unverifiable node (no network info) isn't trusted against a known network", () => {
    expect(choosePin({ name: "Charging station" }, "Rivian Adventure Network")).toEqual({
      snap: false,
      label: "Rivian Adventure Network",
    });
  });
  it("shows an unrecognised ledger network verbatim", () => {
    expect(displayNetwork("  Blink  ")).toBe("Blink");
    expect(choosePin(tesla, "Blink")).toEqual({ snap: false, label: "Blink" });
  });
});
