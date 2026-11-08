import { readFileSync, writeFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import labels from "./chargerLabels.json";
import { ALL_LABEL, CHARGER_KINDS, chargerKindOfKw, chargerLabel, chargerSentence, stationIsDc, stationLine } from "./chargerLabels";
import { AC_MAX_KW, DCFC_MIN_KW } from "./ledger";

// ios/design/charger-labels.json is what the iPhone app's strings are generated from; it must equal this file. Regenerate with
//   UPDATE_CHARGER_LABELS=1 npx vitest run src/lib/chargerLabels.test.ts
const IOS = "../ios/design/charger-labels.json";

describe("charger labels", () => {
  it("the iPhone copy matches", () => {
    if (process.env.UPDATE_CHARGER_LABELS) writeFileSync(IOS, JSON.stringify(labels, null, 2) + "\n");
    expect(JSON.parse(readFileSync(IOS, "utf8"))).toEqual(labels);
  });

  it("every type has a short label and a plain sentence of at most two", () => {
    expect(CHARGER_KINDS).toEqual(["fast", "medium", "hotel", "camping", "outlet"]);
    for (const k of CHARGER_KINDS) {
      expect(chargerLabel(k)).toMatch(/^[A-Z][a-z]+$/);
      const sentence = chargerSentence(k);
      expect(sentence).toMatch(/\.$/);
      expect(sentence.length).toBeLessThan(220);
    }
    expect(ALL_LABEL).toBe("All");
  });

  it("the shorthand lives only in the sentences, never in a label", () => {
    for (const k of CHARGER_KINDS) expect(chargerLabel(k)).not.toMatch(/DC|Level|L2|L1|DCFC/);
  });

  it("power alone: Outlet to 2 kW, Medium up to the AC ceiling, Fast above it (it is DC)", () => {
    expect(chargerKindOfKw(null)).toBeNull();
    expect(chargerKindOfKw(0)).toBeNull();
    expect(chargerKindOfKw(1.4)).toBe("outlet");
    expect(chargerKindOfKw(2)).toBe("outlet");
    expect(chargerKindOfKw(2.1)).toBe("medium");
    expect(chargerKindOfKw(7)).toBe("medium");
    expect(chargerKindOfKw(11.9)).toBe("medium");
    expect(chargerKindOfKw(AC_MAX_KW)).toBe("medium");
    expect(chargerKindOfKw(12.1)).toBe("fast");
    expect(chargerKindOfKw(24)).toBe("fast");
    expect(chargerKindOfKw(49.9)).toBe("fast");
    expect(chargerKindOfKw(DCFC_MIN_KW)).toBe("fast");
    expect(chargerKindOfKw(250)).toBe("fast");
  });

  it("a station's sockets decide AC or DC: a 22 kW AC station is Medium, a 6 kW or 24 kW CCS one is Fast", () => {
    expect(chargerKindOfKw(22, false)).toBe("medium");
    expect(chargerKindOfKw(24, true)).toBe("fast");
    expect(chargerKindOfKw(6, true)).toBe("fast");
    expect(chargerKindOfKw(150, true)).toBe("fast");
    expect(stationIsDc(["type2"])).toBe(false);
    expect(stationIsDc(["type2", "j1772"])).toBe(false);
    expect(stationIsDc(["type2_combo", "chademo"])).toBe(true);
    expect(stationIsDc(["nacs"])).toBeNull();
    expect(stationIsDc([])).toBeNull();
    expect(stationIsDc(undefined)).toBeNull();
  });

  it("the station's rating is said as the station's", () => {
    expect(stationLine("fast", 250)).toBe("Fast · station up to 250 kW");
    expect(stationLine("medium", 11.4)).toBe("Medium · station up to 11 kW");
    expect(stationLine("fast", 24)).toBe("Fast · station up to 24 kW");
    expect(stationLine("hotel")).toBe("Hotel");
  });
});
