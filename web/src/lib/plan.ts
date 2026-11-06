// The plan an account is on, for the paywall, the Settings card and the lapse notice, from the plan fields of GET /api/auth/me.
// Billing off (a self-hosted server, `billing_enabled` false or absent), the demo account, or a failed entitlement lookup all mean
// "off": no plan screen of any kind.

import type { AuthUser } from "./api";

export type PlanFields = Pick<
  AuthUser,
  | "plan"
  | "plan_source"
  | "plan_period_end"
  | "plan_trial_ends"
  | "plan_cancels_at"
  | "plan_grace_ends"
  | "plan_ended_at"
  | "plan_ended_source"
  | "plan_date_basis"
  | "plan_period_end_date"
  | "plan_trial_ends_date"
  | "plan_cancels_date"
  | "plan_grace_ends_date"
  | "plan_ended_date"
  | "billing_enabled"
  | "history_days"
>;

// B8: one billing instant must show one date everywhere. Stripe (and the founding year) show the UTC calendar date of the instant, so with the
// "utc_date" basis the server sends that date ("YYYY-MM-DD") and it is shown as that date, never converted to the reader's zone; Apple's date is the
// device's own, so with the "instant" basis the instant is formatted here. The state carries whichever of the two is to be shown.
function shown(me: PlanFields, instant: string | null | undefined, date: string | null | undefined): string | null {
  return me.plan_date_basis === "utc_date" && date ? date : (instant ?? null);
}

export type PlanState =
  | { kind: "off" } // billing is off: everything is on, nothing mentions a plan
  | { kind: "free" } // never had Pro
  // cancelsAt: set to not renew, Pro stays on until then. graceEnds: a renewal payment failed, Pro stays on until then (7 days).
  | { kind: "pro"; source: "apple" | "stripe" | "founding" | "grant"; periodEnd: string | null; cancelsAt?: string | null; graceEnds?: string | null }
  | { kind: "lapsed"; endedOn: string | null; endedSource: string | null }; // had Pro or a trial, now on the free window

const DAY = 86_400_000;

export function planState(me: PlanFields | null | undefined): PlanState {
  if (!me || me.plan === undefined || me.billing_enabled !== true) return { kind: "off" };
  if (me.plan_source === "demo" || me.plan_source === "billing_off") return { kind: "off" };
  if (me.plan === "pro") {
    // No new account gets a trial (owner decision). An old trial row, if one is still stored, reads as Pro granted to the account.
    const s = me.plan_source;
    const source = s === "apple" || s === "stripe" || s === "founding" || s === "grant" ? s : "grant";
    return {
      kind: "pro",
      source,
      periodEnd: shown(me, me.plan_period_end, me.plan_period_end_date),
      cancelsAt: shown(me, me.plan_cancels_at, me.plan_cancels_date),
      graceEnds: shown(me, me.plan_grace_ends, me.plan_grace_ends_date),
    };
  }
  if (me.plan_ended_at) return { kind: "lapsed", endedOn: shown(me, me.plan_ended_at, me.plan_ended_date), endedSource: me.plan_ended_source ?? null };
  return { kind: "free" };
}

// "March 4, 2027" in the reader's language; "" when the value is not a date. A bare calendar date ("2027-03-04", the server's UTC date) is shown as
// that date in any zone; an instant is shown in the reader's zone.
export function planDate(iso: string | null | undefined): string {
  if (!iso) return "";
  const bare = /^\d{4}-\d{2}-\d{2}$/.test(iso);
  const t = new Date(bare ? `${iso}T00:00:00Z` : iso);
  return Number.isFinite(t.getTime()) ? t.toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric", ...(bare ? { timeZone: "UTC" } : {}) }) : "";
}

// Whole days left until an instant (never negative), for "3 days left".
export function daysLeft(iso: string | null | undefined, now: Date = new Date()): number | null {
  const t = iso ? new Date(iso).getTime() : NaN;
  return Number.isFinite(t) ? Math.max(0, Math.ceil((t - now.getTime()) / DAY)) : null;
}
