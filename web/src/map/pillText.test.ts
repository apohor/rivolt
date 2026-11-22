import { describe, expect, it } from "vitest";
import { chargePillText } from "./pillText";

describe("chargePillText", () => {
  it("is the time and the battery range", () => {
    expect(chargePillText(25, 12.4, 79.6)).toBe("25 min · 12→80%");
  });
});
