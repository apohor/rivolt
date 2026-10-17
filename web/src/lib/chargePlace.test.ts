import { describe, expect, it } from "vitest";
import type { ChargeCluster } from "./api";
import { chargePlaceLabel, chargePlaceLabels } from "./chargePlace";

describe("chargePlaceLabel", () => {
  it("follows the server's kind and place, like Money", () => {
    expect(chargePlaceLabel({ kind: "dcfc", place: "away" })).toBe("Fast");
    expect(chargePlaceLabel({ kind: "dcfc", place: "home" })).toBe("Fast");
    expect(chargePlaceLabel({ kind: "ac", place: "home" })).toBe("Home");
    expect(chargePlaceLabel({ kind: "ac", place: "away" })).toBe("Away");
  });
  it("the server wins over the cluster label", () => {
    // The demo audit's session: an L2 charge 220 mi from home that the
    // clusters called Public and the charge page called "L2 home".
    expect(chargePlaceLabel({ kind: "ac", place: "away" }, "Home")).toBe("Away");
    expect(chargePlaceLabel({ kind: "ac", place: "home" }, "Fast")).toBe("Home");
  });
  it("falls back to the cluster only for what the server couldn't say", () => {
    expect(chargePlaceLabel({ kind: "ac", place: "unknown" }, "Public")).toBe("Away");
    expect(chargePlaceLabel({ kind: "ac", place: "unknown" }, "Fast")).toBe("");
    expect(chargePlaceLabel({}, "Fast")).toBe("Fast");
    expect(chargePlaceLabel({}, "Public")).toBe("Away");
    expect(chargePlaceLabel({})).toBe("");
  });
  it("labels a list by ID", () => {
    const clusters = [{ label: "Public", member_ids: ["b"] } as unknown as ChargeCluster];
    const m = chargePlaceLabels([{ ID: "a", kind: "ac", place: "home" }, { ID: "b" }], clusters);
    expect(m.get("a")).toBe("Home");
    expect(m.get("b")).toBe("Away");
  });
});
