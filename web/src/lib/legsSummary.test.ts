import { describe, expect, it } from "vitest";
import { legsSummary, parkedText } from "./legsSummary";

describe("legsSummary", () => {
  it("lists the parked times", () => {
    expect(legsSummary([{ parkedMinAfter: 13 }, { parkedMinAfter: 19 }, {}])).toBe("3 legs · parked 13 min, 19 min");
  });
  it("counts them past three", () => {
    expect(legsSummary([{ parkedMinAfter: 1 }, { parkedMinAfter: 2 }, { parkedMinAfter: 3 }, { parkedMinAfter: 4 }, {}])).toBe("5 legs · parked 4 times");
  });
  it("says only the leg count with no stops", () => {
    expect(legsSummary([{}, {}])).toBe("2 legs");
  });
  it("writes hours", () => {
    expect(parkedText(65)).toBe("1 h 5 min");
    expect(parkedText(120)).toBe("2 h");
    expect(parkedText(60)).toBe("1 h");
    expect(legsSummary([{ parkedMinAfter: 120 }, {}])).toBe("2 legs · parked 2 h");
  });
});
