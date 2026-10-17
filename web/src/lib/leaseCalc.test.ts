import { describe, expect, it } from "vitest";
import { leaseCalc, type LeaseCalcInput } from "./leaseCalc";

const base: LeaseCalcInput = {
  startDate: "2025-01-01",
  endDate: "2028-01-01", // 1095 days
  allowedMilesTotal: 36000,
  startOdometerMi: 10,
  currentOdometerMi: 10010,
  overagePerMile: 0.25,
};
const day = (s: string) => new Date(`${s}T12:00:00`);

describe("leaseCalc", () => {
  it("mirrors the server for a mid-lease standing", () => {
    // 365 days elapsed of 1095: allowed to date 12,000 (36000*365/1095), used 10,000.
    const r = leaseCalc(base, day("2026-01-01"));
    if (!r.ok) throw new Error("expected ok");
    const st = r.status;
    expect(st.days_total).toBe(1095);
    expect(st.days_elapsed).toBe(365);
    expect(st.days_left).toBe(730);
    expect(st.allowed_to_date_mi).toBe(12000);
    expect(st.used_mi).toBe(10000);
    expect(st.ahead_behind_mi).toBe(2000);
    expect(st.pace_basis).toBe("lease_to_date");
    expect(st.pace_mi_per_day).toBe(27.4); // 10000/365
    expect(st.projected_total_mi).toBe(30000); // 10000 + 27.397*730
    expect(st.projected_return_odometer_mi).toBe(30010);
    expect(st.projected_overage_mi).toBe(0);
    expect(st.projected_overage_cost).toBe(0);
    expect(st.miles_per_day_budget).toBe(35.6); // 26000/730
  });

  it("projects an overage and prices it", () => {
    const r = leaseCalc({ ...base, currentOdometerMi: 20010 }, day("2026-01-01"));
    if (!r.ok) throw new Error("expected ok");
    expect(r.status.projected_total_mi).toBe(60000);
    expect(r.status.projected_overage_mi).toBe(24000);
    expect(r.status.projected_overage_cost).toBe(6000);
    expect(r.status.miles_per_day_budget).toBe(21.9); // 16000/730
  });

  it("gives no projection in the first two weeks", () => {
    const r = leaseCalc({ ...base, currentOdometerMi: 300 }, day("2025-01-10"));
    if (!r.ok) throw new Error("expected ok");
    expect(r.status.days_elapsed).toBe(9);
    expect(r.status.projected_total_mi).toBeNull();
    expect(r.status.pace_mi_per_day).toBeNull();
    expect(r.status.miles_per_day_budget).not.toBeNull();
  });

  it("clamps before the start and after the end", () => {
    const before = leaseCalc(base, day("2024-06-01"));
    const after = leaseCalc({ ...base, currentOdometerMi: 40010 }, day("2029-01-01"));
    if (!before.ok || !after.ok) throw new Error("expected ok");
    expect(before.status.days_elapsed).toBe(0);
    expect(before.status.allowed_to_date_mi).toBe(0);
    expect(after.status.days_left).toBe(0);
    expect(after.status.miles_per_day_budget).toBe(0);
    expect(after.status.projected_total_mi).toBe(40000); // the actual
    expect(after.status.projected_overage_mi).toBe(4000);
    expect(after.status.projected_overage_cost).toBe(1000);
  });

  it("rejects bad input with a message per field", () => {
    const r = leaseCalc({ ...base, endDate: "2024-01-01", allowedMilesTotal: 0, currentOdometerMi: 5, overagePerMile: -1 }, day("2026-01-01"));
    if (r.ok) throw new Error("expected errors");
    expect(Object.keys(r.errors).sort()).toEqual(["allowedMilesTotal", "currentOdometerMi", "endDate", "overagePerMile"]);
  });

  it("rejects empty dates and an over-long term", () => {
    expect(leaseCalc({ ...base, startDate: "" }, day("2026-01-01")).ok).toBe(false);
    expect(leaseCalc({ ...base, endDate: "2040-01-01" }, day("2026-01-01")).ok).toBe(false);
  });
});
