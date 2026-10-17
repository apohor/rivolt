// The public lease-mileage calculator's arithmetic. It mirrors the server's vehiclelease.Compute
// (internal/vehiclelease/lease.go) with the lease-to-date pace, since a visitor has no odometer
// history: the same rounding, the same 14-day minimum before a projection, the same clamping.
// Everything is in miles; the page converts from and to the unit the visitor typed.
import type { LeaseStatus } from "./api";

export const KM_PER_MI = 1.609344;
const MIN_PACE_DAYS = 14;
const MAX_TERM_DAYS = 10 * 366;

export type LeaseCalcInput = {
  startDate: string; // YYYY-MM-DD
  endDate: string;
  allowedMilesTotal: number;
  startOdometerMi: number;
  currentOdometerMi: number;
  overagePerMile: number;
};

export type LeaseCalcResult = { ok: true; status: LeaseStatus } | { ok: false; errors: Partial<Record<keyof LeaseCalcInput, string>> };

const round1 = (f: number) => Math.round(f * 10) / 10;
const DAY = 86_400_000;

// Calendar dates compare as UTC midnights so a time zone or DST shift never moves a day.
function utcDay(s: string): number {
  return /^\d{4}-\d{2}-\d{2}$/.test(s) ? Date.parse(`${s}T00:00:00Z`) : NaN;
}

export function leaseCalc(i: LeaseCalcInput, today: Date): LeaseCalcResult {
  const errors: Partial<Record<keyof LeaseCalcInput, string>> = {};
  const start = utcDay(i.startDate);
  const end = utcDay(i.endDate);
  if (!Number.isFinite(start)) errors.startDate = "Pick the lease start date";
  if (!Number.isFinite(end)) errors.endDate = "Pick the lease end date";
  else if (Number.isFinite(start) && end <= start) errors.endDate = "The end date must be after the start";
  else if (Number.isFinite(start) && (end - start) / DAY > MAX_TERM_DAYS) errors.endDate = "That is longer than 10 years";
  if (!(i.allowedMilesTotal > 0)) errors.allowedMilesTotal = "Enter the total miles the lease allows";
  if (!(i.startOdometerMi >= 0)) errors.startOdometerMi = "Enter the odometer at the lease start (0 or more)";
  if (!(i.currentOdometerMi >= 0)) errors.currentOdometerMi = "Enter the odometer now (0 or more)";
  else if (i.currentOdometerMi < i.startOdometerMi) errors.currentOdometerMi = "The odometer now can't be below the starting odometer";
  if (!(i.overagePerMile >= 0)) errors.overagePerMile = "Enter the overage rate (0 if none)";
  if (Object.keys(errors).length) return { ok: false, errors };

  const todayUTC = Date.UTC(today.getFullYear(), today.getMonth(), today.getDate());
  const total = Math.floor((end - start) / DAY);
  const elapsed = Math.min(Math.max(Math.floor((todayUTC - start) / DAY), 0), total);
  const left = total - elapsed;
  const allowedToDate = round1((i.allowedMilesTotal * elapsed) / total);
  const used = Math.max(0, i.currentOdometerMi - i.startOdometerMi);
  const st: LeaseStatus = {
    used_mi: round1(used),
    allowed_to_date_mi: allowedToDate,
    ahead_behind_mi: round1(allowedToDate - used),
    days_total: total,
    days_elapsed: elapsed,
    days_left: left,
    projected_total_mi: null,
    projected_return_odometer_mi: null,
    projected_overage_mi: null,
    projected_overage_cost: null,
    miles_per_day_budget: left > 0 ? round1(Math.max(0, (i.allowedMilesTotal - used) / left)) : 0,
    pace_mi_per_day: null,
    odometer_mi: round1(i.currentOdometerMi),
    odometer_at: null,
  };
  let pace: number;
  if (elapsed >= MIN_PACE_DAYS) {
    pace = used / elapsed;
    st.pace_basis = "lease_to_date";
    st.pace_mi_per_day = round1(pace);
  } else if (left === 0) {
    pace = 0; // over: the projection is the actual
  } else {
    return { ok: true, status: st }; // too early in the lease for a pace
  }
  const projected = used + pace * left;
  const over = Math.max(0, projected - i.allowedMilesTotal);
  st.projected_total_mi = round1(projected);
  st.projected_return_odometer_mi = round1(i.startOdometerMi + projected);
  st.projected_overage_mi = round1(over);
  st.projected_overage_cost = Math.round(over * i.overagePerMile * 100) / 100;
  return { ok: true, status: st };
}
