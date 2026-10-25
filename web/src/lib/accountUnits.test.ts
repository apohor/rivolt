import { describe, expect, it } from "vitest";
import { reconcile, regionDistanceUnit, type Local } from "./accountUnits";
import type { AccountUnits } from "./api";

const acct = (o: Partial<AccountUnits>): AccountUnits => ({ distance_unit: "mi", temperature_unit: "f", distance_source: "unset", temperature_source: "follows_distance", ...o });
const local = (o: Partial<Local>): Local => ({ distanceUnit: "mi", temperatureUnit: "f", temperatureUnitSet: false, picked: false, ...o });

describe("regionDistanceUnit", () => {
  it("is miles for the US, Liberia, Myanmar and the UK, kilometres elsewhere, and null without a region", () => {
    expect(regionDistanceUnit("en-US")).toBe("mi");
    expect(regionDistanceUnit("en-GB")).toBe("mi");
    expect(regionDistanceUnit("my-MM")).toBe("mi");
    expect(regionDistanceUnit("en-LR")).toBe("mi");
    expect(regionDistanceUnit("de-DE")).toBe("km");
    expect(regionDistanceUnit("fr-CA")).toBe("km");
    expect(regionDistanceUnit("en-AU")).toBe("km");
    expect(regionDistanceUnit("")).toBeNull();
    expect(regionDistanceUnit(undefined)).toBeNull();
  });
});

describe("reconcile", () => {
  it("an account with a value wins and the local copy is dropped", () => {
    const p = reconcile(acct({ distance_unit: "km", temperature_unit: "c", distance_source: "user" }), local({ picked: true, distanceUnit: "mi", temperatureUnitSet: true, temperatureUnit: "f" }), "mi");
    expect(p).toEqual({ apply: { distance: "km", temperature: "c", temperatureSet: false } });
    const user = reconcile(acct({ distance_unit: "km", temperature_unit: "f", distance_source: "user", temperature_source: "user" }), local({}), null);
    expect(user.apply).toEqual({ distance: "km", temperature: "f", temperatureSet: true });
    expect(user.patch).toBeUndefined();
  });
  it("a region-seeded value counts as the account's", () => {
    expect(reconcile(acct({ distance_unit: "km", temperature_unit: "c", distance_source: "region" }), local({ picked: true }), "mi").patch).toBeUndefined();
  });
  it("an unset account takes this browser's choice once, as a normal pick", () => {
    expect(reconcile(acct({}), local({ picked: true, distanceUnit: "km" }), "mi")).toEqual({ patch: { distance_unit: "km" } });
    expect(reconcile(acct({}), local({ picked: true, distanceUnit: "km", temperatureUnitSet: true, temperatureUnit: "f" }), "mi")).toEqual({ patch: { distance_unit: "km", temperature_unit: "f" } });
  });
  it("an unset account with no local choice is seeded once from the region, and from nothing when there is none", () => {
    expect(reconcile(acct({}), local({}), "km")).toEqual({ patch: { seed: true, distance_unit: "km" }, apply: { distance: "km", temperature: "c", temperatureSet: false } });
    expect(reconcile(acct({}), local({}), null)).toEqual({});
  });
});
