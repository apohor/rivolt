import { describe, expect, it } from "vitest";
import { chargePillText } from "./pillText";

describe("chargePillText", () => {
  it("is the time and the battery range, as before", () => {
    expect(chargePillText(25, 12.4, 79.6)).toBe("25 min · 12→80%");
    expect(chargePillText(25, 12.4, 79.6, null)).toBe("25 min · 12→80%");
  });
  it("leads with the stop number when there is one", () => {
    expect(chargePillText(25, 12, 80, 2)).toBe("2 · 25 min · 12→80%");
  });
});
