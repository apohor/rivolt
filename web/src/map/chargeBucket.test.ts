import { describe, expect, it } from "vitest";
import { chargeBucket } from "./ChargesOverviewMap";

describe("charge buckets on the overview map", () => {
  it("anything over the AC ceiling is Fast, with the peak shown next to it; Level 2 stays Medium", () => {
    expect(chargeBucket({ MaxPowerKW: 11.9, kind: "ac" })).toBe("l2");
    expect(chargeBucket({ MaxPowerKW: 12.1, kind: "ac" })).toBe("fast");
    expect(chargeBucket({ MaxPowerKW: 24, kind: "ac" })).toBe("fast");
    expect(chargeBucket({ MaxPowerKW: 49.9, kind: "ac" })).toBe("fast");
    expect(chargeBucket({ MaxPowerKW: 40, kind: "dcfc" })).toBe("fast");
    expect(chargeBucket({ MaxPowerKW: 50 })).toBe("fast");
    expect(chargeBucket({ MaxPowerKW: 2.1, kind: "ac" })).toBe("l1");
    expect(chargeBucket({ MaxPowerKW: 1.4, kind: "ac" })).toBe("l1");
  });
});
