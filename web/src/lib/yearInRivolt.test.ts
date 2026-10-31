import { describe, expect, it } from "vitest";
import full from "../../../ios/RivoltTests/Fixtures/year_in_rivolt.json";
import notYet from "../../../ios/RivoltTests/Fixtures/year_in_rivolt_not_yet.json";
import type { YearInRivolt } from "./api";
import { buildYearShareModel, monthName, yearCardYear, yearFileName } from "./yearInRivolt";

const y = full as YearInRivolt;

describe("the year card", () => {
  it("is offered only in January, for the year just ended", () => {
    expect(yearCardYear(new Date(2027, 0, 5))).toBe(2026);
    expect(yearCardYear(new Date(2027, 0, 31))).toBe(2026);
    expect(yearCardYear(new Date(2026, 9, 30))).toBeNull();
    expect(yearCardYear(new Date(2026, 11, 31))).toBeNull();
  });

  it("draws nothing for an ineligible answer", () => {
    expect(buildYearShareModel(notYet as YearInRivolt, { includeCost: false })).toBeNull();
  });

  it("has the model in the title, 'since' for a partial year, and no money by default", () => {
    const m = buildYearShareModel(y, { includeCost: false })!;
    expect(m.title).toBe("2026 in your R1S");
    expect(m.eyebrow).toBe("Recorded by Rivolt · since March");
    expect(m.subtitle).toBe("612 drives · 96 charges · 19 stops on 7 planned trips");
    expect(m.stats.length).toBeLessThanOrEqual(6);
    const text = JSON.stringify(m);
    expect(text).not.toMatch(/[$€£]|USD|cost/i);
    expect(m.notes).toEqual([]);
  });

  it("labels energy as used driving, and the efficiency as an estimate", () => {
    const rows = Object.fromEntries(buildYearShareModel(y, { includeCost: false })!.stats.map((s) => [s.label, s.value]));
    expect(rows["kWh used driving"]).toBe("3,012 kWh");
    expect(rows["Efficiency"]).toBe("about 2.8 mi/kWh");
    expect(rows["Longest trip"]).toBe("413 mi · July");
    expect(rows["Charged"]).toBe("62% home · 11% away · 27% fast");
    expect(rows["Coldest drive"]).toBe("4°F · January");
  });

  it("adds the cost, labelled an estimate with its per-mile, only when asked", () => {
    const m = buildYearShareModel(y, { includeCost: true })!;
    const cost = m.stats.find((s) => s.label === "Cost (estimate)");
    expect(cost?.value).toBe("~$611 · ~$0.07/mi");
    expect(m.notes).toEqual([]);
    expect(m.stats.length).toBeLessThanOrEqual(6);
  });

  it("hides the lines it has nothing true to say for", () => {
    const m = buildYearShareModel(
      { ...y, model: undefined, since_month: undefined, planned_trips: 0, planned_stops: 0, coldest_drive: undefined, most_efficient_long_drive: undefined, cost: undefined, cost_per_mile: undefined },
      { includeCost: true },
    )!;
    expect(m.title).toBe("2026 in Rivolt");
    expect(m.eyebrow).toBe("Recorded by Rivolt");
    expect(m.subtitle).toBe("612 drives · 96 charges");
    expect(m.stats.map((s) => s.label)).not.toContain("Coldest drive");
    expect(JSON.stringify(m)).not.toMatch(/\$|stops on/);
  });

  it("never shows a malformed month", () => {
    expect(monthName("2026-13")).toBe("");
    expect(monthName(undefined)).toBe("");
    expect(monthName("2026-07")).toBe("July");
    expect(yearFileName(2026)).toBe("rivolt-year-2026.png");
  });
});
