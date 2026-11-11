import { readFileSync, writeFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import copy from "./vehicleDiscovery.json";
import { discoveryStateOf, discoveryView } from "./vehicleDiscovery";

// ios/design/vehicle-discovery.json is what the iPhone app's strings are generated from; it must equal this file. Regenerate with
//   UPDATE_VEHICLE_DISCOVERY=1 npx vitest run src/lib/vehicleDiscovery.test.ts
const IOS = "../ios/design/vehicle-discovery.json";

describe("vehicle discovery copy", () => {
  it("the iPhone copy matches", () => {
    if (process.env.UPDATE_VEHICLE_DISCOVERY) writeFileSync(IOS, JSON.stringify(copy, null, 2) + "\n");
    expect(JSON.parse(readFileSync(IOS, "utf8"))).toEqual(copy);
  });

  it("each state says one plain thing, with no number promised and no blame", () => {
    for (const s of ["looking", "none", "reauth"] as const) {
      const v = discoveryView(s)!;
      expect(v.title.length).toBeLessThan(60);
      expect(v.body).toMatch(/\.$/);
      expect(`${v.title} ${v.body}`).not.toMatch(/error|failed|wrong|sorry/i);
    }
    expect(discoveryView("none")!.title).toBe("No vehicles on this Rivian account yet");
    expect(discoveryView("none")!.action).toBe("Open Rivian settings");
    expect(discoveryView("reauth")!.action).toBe("Reconnect Rivian");
    expect(discoveryView("looking")!.action).toBeUndefined();
  });

  it("the states the page already handles say nothing new", () => {
    expect(discoveryView("unlinked")).toBeNull();
    expect(discoveryView("found")).toBeNull();
    expect(discoveryView(null)).toBeNull();
    expect(discoveryView(undefined)).toBeNull();
  });

  it("the server's answer wins; a 503 is looking; anything else is no word", () => {
    expect(discoveryStateOf({ state: "reauth" })).toBe("reauth");
    expect(discoveryStateOf({ state: "none" })).toBe("none");
    expect(discoveryStateOf(undefined, 503)).toBe("looking");
    expect(discoveryStateOf(undefined, 404)).toBeNull();
    expect(discoveryStateOf(undefined)).toBeNull();
    expect(discoveryStateOf({ state: "nonsense" })).toBeNull();
  });
});
