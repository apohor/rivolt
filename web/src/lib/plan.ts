// The plan an account is on, for the paywall, the Settings card and the lapse notice, from the plan fields of GET /api/auth/me.
// Billing off (a self-hosted server, `billing_enabled` false or absent), the demo account, or a failed entitlement lookup all mean
// "off": no plan screen of any kind.

import type { AuthUser } from "./api";

export type PlanFields = Pick<
  AuthUser,
  "plan" | "plan_source" | "plan_period_end" | "plan_trial_ends" | "plan_cancels_at" | "plan_grace_ends" | "plan_ended_at" | "plan_ended_source" | "billing_enabled" | "history_days"
>;

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
    return { kind: "pro", source, periodEnd: me.plan_period_end ?? null, cancelsAt: me.plan_cancels_at ?? null, graceEnds: me.plan_grace_ends ?? null };
  }
  if (me.plan_ended_at) return { kind: "lapsed", endedOn: me.plan_ended_at, endedSource: me.plan_ended_source ?? null };
  return { kind: "free" };
}

// "March 4, 2027" in the reader's language; "" when the value is not a date.
export function planDate(iso: string | null | undefined): string {
  const t = iso ? new Date(iso) : null;
  return t && Number.isFinite(t.getTime()) ? t.toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" }) : "";
}

// Whole days left until an instant (never negative), for "3 days left".
export function daysLeft(iso: string | null | undefined, now: Date = new Date()): number | null {
  const t = iso ? new Date(iso).getTime() : NaN;
  return Number.isFinite(t) ? Math.max(0, Math.ceil((t - now.getTime()) / DAY)) : null;
}
