import { readFileSync, writeFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import labels from "./chargerLabels.json";
import { ALL_LABEL, CHARGER_KINDS, chargerKindOfKw, chargerLabel, chargerSentence, stationLine } from "./chargerLabels";
import { DCFC_MIN_KW } from "./ledger";

// ios/design/charger-labels.json is what the iPhone app's strings are generated from; it must equal this file. Regenerate with
//   UPDATE_CHARGER_LABELS=1 npx vitest run src/lib/chargerLabels.test.ts
const IOS = "../ios/design/charger-labels.json";

describe("charger labels", () => {
  it("the iPhone copy matches", () => {
    if (process.env.UPDATE_CHARGER_LABELS) writeFileSync(IOS, JSON.stringify(labels, null, 2) + "\n");
    expect(JSON.parse(readFileSync(IOS, "utf8"))).toEqual(labels);
  });

  it("every type has a one-word label and a plain sentence", () => {
    expect(CHARGER_KINDS).toEqual(["fast", "medium", "hotel", "camping", "outlet"]);
    for (const k of CHARGER_KINDS) {
      expect(chargerLabel(k)).toMatch(/^[A-Z][a-z]+$/);
      expect(chargerSentence(k).length).toBeGreaterThan(40);
      expect(chargerSentence(k)).toMatch(/\.$/);
    }
    expect(ALL_LABEL).toBe("All");
  });

  it("the shorthand lives only in the sentences, never in a label", () => {
    for (const k of CHARGER_KINDS) expect(chargerLabel(k)).not.toMatch(/DC|Level|L2|DCFC/);
  });

  it("a rating maps to Outlet, Medium or Fast at the ledger's DC threshold", () => {
    expect(chargerKindOfKw(null)).toBeNull();
    expect(chargerKindOfKw(0)).toBeNull();
    expect(chargerKindOfKw(1.4)).toBe("outlet");
    expect(chargerKindOfKw(7)).toBe("medium");
    expect(chargerKindOfKw(DCFC_MIN_KW - 1)).toBe("medium");
    expect(chargerKindOfKw(DCFC_MIN_KW)).toBe("fast");
    expect(chargerKindOfKw(250)).toBe("fast");
  });

  it("the station's rating is said as the station's", () => {
    expect(stationLine("fast", 250)).toBe("Fast · station up to 250 kW");
    expect(stationLine("medium", 11.4)).toBe("Medium · station up to 11 kW");
    expect(stationLine("hotel")).toBe("Hotel");
  });
});
