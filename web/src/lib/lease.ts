// Lease tracker helpers: the bar geometry and the plain-words summary from the server's status,
// and form validation for the terms. The server owns the arithmetic; this only presents it.
import type { LeaseStatus, LeaseTerms } from "./api";

export type LeaseTone = "ok" | "watch" | "over";

export type LeaseBar = {
  // 0..100 positions on a track whose full width is max(total allowance, projected total).
  usedPct: number;
  planPct: number;
  allowedPct: number;
  projectedPct: number | null;
  tone: LeaseTone;
};

export function leaseBar(st: LeaseStatus, allowedTotal: number): LeaseBar {
  const used = st.used_mi ?? 0;
  const proj = st.projected_total_mi;
  const scale = Math.max(allowedTotal, proj ?? 0, used, 1);
  const pct = (v: number) => Math.max(0, Math.min(100, (v / scale) * 100));
  const tone: LeaseTone = used > allowedTotal ? "over" : used > st.allowed_to_date_mi ? "watch" : "ok";
  return { usedPct: pct(used), planPct: pct(st.allowed_to_date_mi), allowedPct: pct(allowedTotal), projectedPct: proj == null ? null : pct(proj), tone };
}

// "1,720 mi under plan" / "340 mi over plan" / "on plan".
export function planStanding(st: LeaseStatus): { text: string; tone: LeaseTone } | null {
  if (st.ahead_behind_mi == null) return null;
  const d = Math.round(Math.abs(st.ahead_behind_mi));
  if (d === 0) return { text: "on plan", tone: "ok" };
  return st.ahead_behind_mi > 0 ? { text: `${d.toLocaleString()} mi under plan`, tone: "ok" } : { text: `${d.toLocaleString()} mi over plan`, tone: "watch" };
}

export type LeaseErrors = Partial<Record<keyof LeaseTerms, string>>;

export function validateLease(t: Partial<Record<keyof LeaseTerms, string>>): LeaseErrors {
  const e: LeaseErrors = {};
  const start = Date.parse(t.start_date ?? "");
  const end = Date.parse(t.end_date ?? "");
  if (!Number.isFinite(start)) e.start_date = "Pick the lease start date";
  if (!Number.isFinite(end)) e.end_date = "Pick the lease end date";
  else if (Number.isFinite(start) && end <= start) e.end_date = "The end date must be after the start";
  const n = (v: string | undefined) => (v === undefined || v.trim() === "" ? NaN : Number(v));
  if (!(n(t.allowed_miles_total) > 0)) e.allowed_miles_total = "Enter the total miles the lease allows";
  const so = n(t.start_odometer_mi);
  if (!(so >= 0)) e.start_odometer_mi = "Enter the odometer at the lease start (0 or more)";
  const op = n(t.overage_per_mile);
  if (!(op >= 0)) e.overage_per_mile = "Enter the overage rate per mile (0 if none)";
  return e;
}
