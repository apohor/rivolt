import { describe, expect, it } from "vitest";
import { hasOpenNetworkState, isOpenNetworkState, rateBasisLabel, whyLine } from "./autoNetwork";

describe("autoNetwork", () => {
  it("builds the why line from candidates", () => {
    expect(
      whyLine({
        source: "archive",
        candidates: [
          { network: "Tesla Supercharger", dist_m: 38 },
          { network: "Rivian Adventure Network", dist_m: 112 },
        ],
      }),
    ).toBe("Nearest: Tesla Supercharger, 38 m; also Rivian Adventure Network, 112 m");
  });
  it("notes an OSM disagreement and long distances", () => {
    expect(
      whyLine({ source: "archive", candidates: [{ network: "EVgo", dist_m: 1500 }], osm: { network: "ChargePoint", agree: false } }),
    ).toBe("Nearest: EVgo, 1.5 km. The map lists it as ChargePoint");
  });
  it("explains site memory and Rivian chargers, and is null without evidence", () => {
    expect(whyLine({ source: "site_memory" })).toMatch(/confirmed this network/);
    expect(whyLine({ source: "rivian_charger" })).toMatch(/Rivian/);
    expect(whyLine(null)).toBeNull();
    expect(whyLine({ source: "none", candidates: [] })).toBeNull();
  });
  it("treats null as feature off and confirmed as closed", () => {
    expect(isOpenNetworkState(null)).toBe(false);
    expect(isOpenNetworkState("confirmed")).toBe(false);
    expect(isOpenNetworkState("needs_confirmation")).toBe(true);
    expect(hasOpenNetworkState({ network_state: null, sessions: [{ network_state: "estimated" }] })).toBe(true);
  });
  it("labels the rate basis", () => {
    expect(rateBasisLabel("member")).toBe("member");
    expect(rateBasisLabel(undefined)).toBeNull();
  });
});
