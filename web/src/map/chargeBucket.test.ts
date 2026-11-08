import { describe, expect, it } from "vitest";
import { chargeBucket } from "./ChargesOverviewMap";

describe("charge buckets on the overview map", () => {
  it("DC under the fast threshold is Slow DC, not Level 2", () => {
    expect(chargeBucket({ MaxPowerKW: 11.9, kind: "ac" })).toBe("l2");
    expect(chargeBucket({ MaxPowerKW: 12.1, kind: "ac" })).toBe("slowdc");
    expect(chargeBucket({ MaxPowerKW: 24, kind: "ac" })).toBe("slowdc");
    expect(chargeBucket({ MaxPowerKW: 49.9, kind: "ac" })).toBe("slowdc");
    expect(chargeBucket({ MaxPowerKW: 40, kind: "dcfc" })).toBe("fast");
    expect(chargeBucket({ MaxPowerKW: 50 })).toBe("fast");
    expect(chargeBucket({ MaxPowerKW: 1.4, kind: "ac" })).toBe("l1");
  });
});
