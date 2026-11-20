import { describe, expect, it } from "vitest";
import type { TripReceipt } from "./api";
import { buildReceiptModel, receiptText, RECEIPT_FILE } from "./tripReceipt";

// What the server returns: areas only, a named public charger, an added stop as "Stop · <area>".
const R: TripReceipt = {
  depart_at: "2026-11-21T15:00:00Z",
  tz: "America/Los_Angeles",
  start_soc: 90,
  start: "Pasadena area",
  end: "Las Vegas area",
  rows: [
    { kind: "drive", from: "Pasadena area", to: "Example Fast Charge", dist_m: 160934, dur_s: 6000, from_soc: 90, to_soc: 41, rounded: true },
    { kind: "charge", name: "Example Fast Charge · Barstow", dur_s: 1080, from_soc: 41, to_soc: 80, max_kw: 250 },
    { kind: "stop", name: "Stop · Baker area", from_soc: 60, to_soc: 60 },
    { kind: "drive", from: "Example Fast Charge", to: "Las Vegas area", dist_m: 241401, dur_s: 8400, from_soc: 80, to_soc: 30, rounded: true },
  ],
  totals: { dist_m: 402335, drive_s: 14400, charge_s: 1080, stops: 1, arrive_at: "2026-11-21T21:00:00Z", arrival_soc: 30 },
  sketch: ["_p~iF~ps|U_ulLnnqC"],
};
const o = { model: "R1S", unit: "mi" as const, includeSketch: false, includeCost: false, viewerTZ: "America/Chicago" };

describe("buildReceiptModel", () => {
  it("says only areas, the model and the footer, never a stored label", () => {
    const t = receiptText(buildReceiptModel(R, o));
    expect(t).toContain("Pasadena area → Las Vegas area");
    expect(t).toContain("R1S");
    expect(t).toContain("Stop · Baker area");
    expect(t).toContain("Unofficial, not affiliated with Rivian.");
    for (const bad of ["Home", "Sample destination", "Motel", "@", "http", "QR"]) expect(t).not.toContain(bad);
  });
  it("marks rounded figures and keeps the file name free of place and date", () => {
    const m = buildReceiptModel(R, o);
    expect(m.lines[0].title).toContain("~100 mi");
    expect(m.big[0]).toBe("~250 mi");
    expect(RECEIPT_FILE).toBe("rivolt-trip-plan.png");
  });
  it("draws the sketch only when asked for and sent", () => {
    expect(buildReceiptModel(R, o).sketch).toBeNull();
    expect(buildReceiptModel(R, { ...o, includeSketch: true }).sketch).toEqual(R.sketch);
    expect(buildReceiptModel({ ...R, sketch: [] }, { ...o, includeSketch: true }).sketch).toBeNull();
  });
  it("adds the cost only when asked for, as an estimate", () => {
    const cost = { currency: "USD", dcfc_spend_low: 10, dcfc_spend_high: 14 } as never;
    expect(buildReceiptModel(R, { ...o, cost }).cost).toBeNull();
    expect(buildReceiptModel(R, { ...o, includeCost: true, cost }).cost?.label).toMatch(/estimate/);
  });
  it("states the zone of the leave time and of the arrival, so a trip across zones reconciles", () => {
    const m = buildReceiptModel(R, o);
    // Leave 15:00Z in Chicago (CST, UTC-6) and arrive 21:00Z in Los Angeles (PST, UTC-8): 9:00 AM CST + 6 h = 3:00 PM CST = 1:00 PM PST.
    expect(m.sub).toMatch(/9:00\sAM\sCST/);
    expect(m.totals.find((t) => t.label === "Arrive")?.value).toMatch(/1:00\sPM\sPST/);
  });
  it("shows how long an added stop lasts and drops an unchanged battery line", () => {
    const m = buildReceiptModel({ ...R, rows: [{ kind: "stop", name: "Stop · Baker area", dur_s: 1800, from_soc: 60, to_soc: 60 }, { kind: "stop", name: "Stop · Baker area", from_soc: 60, to_soc: 58 }] }, o);
    expect(m.lines[0].right).toBe("30 min");
    expect(m.lines[0].subRight).toBe("");
    expect(m.lines[1].right).toBe("");
    expect(m.lines[1].subRight).toBe("60% → 58%");
  });
});
