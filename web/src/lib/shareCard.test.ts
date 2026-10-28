import { describe, expect, it, vi } from "vitest";
import * as config from "./config";
import type { Drive, MilestoneItem } from "./api";
import type { LedgerMonth } from "./ledger";
import { buildMilestoneShareModel, buildMonthShareModel, buildTripShareModel, shareFileName, shareFooter, SHARE_FOOTER } from "./shareCard";

const month = {
  period: "2026-10",
  range: { start: "2026-10-01T00:00:00Z", end: "2026-11-01T00:00:00Z" },
  currency: "USD",
  is_current_month: true,
  spend: { total: 42.5, by_source: {}, priced_sessions: 3, unpriced_sessions: 0, unpriced_kwh: 0, unpriced_home_rate_estimate: null },
  energy: { sessions: 5, kwh_added: 210.4, null_energy_sessions: 0, by_kind: { ac: { cost: 20, kwh: 100, sessions: 3 }, dcfc: { cost: 22.5, kwh: 110, sessions: 2 }, unknown: { cost: 0, kwh: 0, sessions: 0 } } },
  driving: { drives: 12, miles: 412.3, miles_with_energy: 400, kwh_used_known: 150, coverage: 1, cost_per_mile: 0.103, cost_per_mile_basis: "spend_over_miles" },
} as unknown as LedgerMonth;
const drive = { ID: "drv-secret-1", VehicleID: "veh-secret", StartedAt: "2026-10-19T14:32:00Z", EndedAt: "2026-10-19T15:47:00Z", DistanceMi: 51.2, EnergyUsedKWh: 17, StartSoCPct: 80, EndSoCPct: 62, AvgSpeedMph: 41, StartLat: 30.1234, StartLon: -97.5678, EndLat: 30.2, EndLon: -97.6, estimated_cost: 2.4, estimated_currency: "USD" } as unknown as Drive;
const ms = (o: Partial<MilestoneItem>): MilestoneItem => ({ vehicle_id: "veh-secret", key: "k", kind: "miles_1000", achieved_at: "2026-10-10T12:00:00Z", title: "1,000 miles", body: "First 1,000 miles recorded", ...o });

const all = (m: { title: string; subtitle: string; eyebrow: string; stats: { label: string; value: string }[]; notes: string[] }) => JSON.stringify(m);

describe("share card models", () => {
  it("month: no dollars unless cost is on", () => {
    const off = buildMonthShareModel(month, { includeCost: false, sample: false });
    expect(all(off)).not.toMatch(/\$|USD/);
    expect(off.eyebrow).toContain("so far");
    const on = buildMonthShareModel(month, { includeCost: true, sample: false });
    expect(on.title).toMatch(/\$/);
  });
  it("trip: the day only, no time of day, id, coordinate or place", () => {
    const m = buildTripShareModel(drive, { includeCost: false, sample: false });
    const j = all(m);
    expect(j).not.toMatch(/drv-secret|veh-secret|30\.12|97\.56|\$/);
    expect(j).not.toMatch(/\d{1,2}:\d{2}/);
    expect(m.title).toContain("51 mi");
    expect(m.title).toContain("1 h 15 min");
    expect(all(buildTripShareModel(drive, { includeCost: true, sample: false }))).toMatch(/~\$2\.40/);
  });
  it("milestone: the server's words, never a dollar figure, no ids", () => {
    const m = buildMilestoneShareModel(ms({}), { sample: false });
    expect(all(m)).not.toMatch(/veh-secret/);
    expect(m.subtitle).toContain("First 1,000 miles recorded");
    const priced = buildMilestoneShareModel(ms({ body: "$0.12/mi over 1,000 miles" }), { sample: false });
    expect(priced.subtitle).not.toMatch(/\$/);
  });
  it("the models only have display fields", () => {
    for (const m of [buildMonthShareModel(month, { includeCost: true, sample: true }), buildTripShareModel(drive, { includeCost: true, sample: true }), buildMilestoneShareModel(ms({}), { sample: true })]) {
      expect(Object.keys(m).sort()).toEqual(["eyebrow", "kind", "notes", "sample", "stats", "subtitle", "title"]);
    }
  });
  it("file names are generic: the kind and the month", () => {
    expect(shareFileName("trip", "2026-10-19T14:32:00")).toBe("rivolt-trip-2026-10.png");
    expect(shareFileName("month", "garbage")).toBe("rivolt-month-card.png");
  });
  it("the footer is the approved plain text", () => {
    expect(SHARE_FOOTER).toEqual(["Made with Rivolt · rivolt.dev", "Unofficial. Not affiliated with Rivian."]);
  });
});

describe("off-peak share line", () => {
  it("is absent unless asked for", () => {
    expect(all(buildMonthShareModel(month, { includeCost: false, sample: false }))).not.toMatch(/off-peak/i);
    expect(all(buildMonthShareModel(month, { includeCost: false, sample: false, offpeakSharePct: null }))).not.toMatch(/off-peak/i);
  });
  it("is a rounded percentage only: no dollars, rate or times, with or without cost", () => {
    for (const includeCost of [false, true]) {
      const m = buildMonthShareModel(month, { includeCost, sample: false, offpeakSharePct: 61.6 });
      expect(m.stats).toContainEqual({ label: "Home charging off-peak", value: "62%" });
      const line = m.stats.find((s) => /off-peak/i.test(s.label))!;
      expect(`${line.label} ${line.value}`).not.toMatch(/\$|rate|kWh|AM|PM|:/);
    }
  });
  it("keeps the percentage inside 0 to 100", () => {
    expect(buildMonthShareModel(month, { includeCost: false, sample: false, offpeakSharePct: 130 }).stats.at(-1)?.value).toBe("100%");
    expect(buildMonthShareModel(month, { includeCost: false, sample: false, offpeakSharePct: -3 }).stats.at(-1)?.value).toBe("0%");
  });
});

describe("estimated energy on the month card", () => {
  it("marks the energy total with a tilde only when the month includes an estimated charge", () => {
    const all = (m: ReturnType<typeof buildMonthShareModel>) => m.stats.map((s) => `${s.label} ${s.value}`).join(" | ");
    expect(all(buildMonthShareModel(month, { includeCost: false, sample: false }))).not.toMatch(/Energy added ~/);
    const est = { ...month, energy: { ...month.energy, includes_estimated: true } } as LedgerMonth;
    expect(all(buildMonthShareModel(est, { includeCost: false, sample: false }))).toMatch(/Energy added ~\d+ kWh/);
  });
});

describe("the share card footer names the app only once it is on the App Store", () => {
  const withUrl = (iosAppUrl: string) =>
    vi.spyOn(config, "getConfig").mockReturnValue({ features: { iosAppUrl } } as unknown as ReturnType<typeof config.getConfig>);
  it("is the plain footer until then, and for TestFlight", () => {
    withUrl("");
    expect(shareFooter()).toEqual(SHARE_FOOTER);
    withUrl("https://testflight.apple.com/join/abc123");
    expect(shareFooter()).toEqual(SHARE_FOOTER);
    vi.restoreAllMocks();
  });
  it("adds the clause for an App Store link, keeping the unofficial line", () => {
    withUrl("https://apps.apple.com/us/app/rivolt/id1234567890");
    expect(shareFooter()).toEqual(["Made with Rivolt · rivolt.dev · iPhone app on the App Store", "Unofficial. Not affiliated with Rivian."]);
    vi.restoreAllMocks();
  });
});
