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
    expect(CHARGER_KINDS).toEqual(["fast", "slowdc", "medium", "hotel", "camping", "outlet"]);
    for (const k of CHARGER_KINDS) {
      expect(chargerLabel(k)).toMatch(/^[A-Z][a-z]+( DC)?$/);
      const sentence = chargerSentence(k);
      expect(sentence).toMatch(/\.$/);
      expect(sentence.split(/\.\s+/).length).toBeLessThanOrEqual(2);
      expect(sentence.length).toBeLessThan(170);
    }
    expect(ALL_LABEL).toBe("All");
  });

  it("the shorthand is in a label only where it is the point (Slow DC), never L2 or DCFC", () => {
    for (const k of CHARGER_KINDS) expect(chargerLabel(k)).not.toMatch(/Level|L2|L1|DCFC/);
    expect(CHARGER_KINDS.filter((k) => /DC/.test(chargerLabel(k)))).toEqual(["slowdc"]);
  });

  it("power alone: Outlet to 2 kW, Medium up to the AC ceiling, Slow DC between, Fast from the DC threshold", () => {
    expect(chargerKindOfKw(null)).toBeNull();
    expect(chargerKindOfKw(0)).toBeNull();
    expect(chargerKindOfKw(1.4)).toBe("outlet");
    expect(chargerKindOfKw(7)).toBe("medium");
    expect(chargerKindOfKw(11.9)).toBe("medium");
    expect(chargerKindOfKw(AC_MAX_KW)).toBe("medium");
    expect(chargerKindOfKw(12.1)).toBe("slowdc");
    expect(chargerKindOfKw(24)).toBe("slowdc");
    expect(chargerKindOfKw(49.9)).toBe("slowdc");
    expect(chargerKindOfKw(DCFC_MIN_KW)).toBe("fast");
    expect(chargerKindOfKw(250)).toBe("fast");
  });

  it("a station's sockets decide AC or DC: a 22 kW AC station is Medium, a 24 kW CCS one is Slow DC", () => {
    expect(chargerKindOfKw(22, false)).toBe("medium");
    expect(chargerKindOfKw(24, true)).toBe("slowdc");
    expect(chargerKindOfKw(6, true)).toBe("slowdc");
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
    expect(stationLine("hotel")).toBe("Hotel");
  });
});
