// The plan an account is on, for the paywall, the Settings card and the lapse notice. MOCKED against the shape in paid-tier-plan.md
// section 3 ({plan, period_end, source, trial_ends} on the account) until the billing API merges; nothing here switches billing on.
// With no `plan` on the account (billing off, a self-hosted server, the demo) every function answers "no plan screens".

import type { AuthUser } from "./api";

export type PlanState =
  | { kind: "off" } // billing is off: everything is on, nothing mentions a plan
  | { kind: "free" } // never had Pro
  | { kind: "trial"; ends: string }
  | { kind: "pro"; source: "apple" | "stripe" | "founding" | "grant"; periodEnd: string | null }
  | { kind: "lapsed"; endedOn: string | null }; // had Pro or a trial, now on the free window

const DAY = 86_400_000;

export function planState(me: Pick<AuthUser, "plan" | "source" | "period_end" | "trial_ends"> | null | undefined, now: Date = new Date()): PlanState {
  if (!me || me.plan === undefined) return { kind: "off" };
  const at = now.getTime();
  const when = (iso: string | null | undefined) => {
    const t = iso ? new Date(iso).getTime() : NaN;
    return Number.isFinite(t) ? t : null;
  };
  if (me.plan === "pro") {
    if (me.source === "trial") return { kind: "trial", ends: me.trial_ends ?? me.period_end ?? "" };
    const src = me.source === "apple" || me.source === "stripe" || me.source === "founding" || me.source === "grant" ? me.source : "grant";
    return { kind: "pro", source: src, periodEnd: me.period_end ?? null };
  }
  // Free: a lapse when a period or a trial has ended; otherwise never had Pro.
  const ended = [when(me.period_end), when(me.trial_ends)].filter((t): t is number => t !== null && t <= at);
  if (ended.length > 0) return { kind: "lapsed", endedOn: new Date(Math.max(...ended)).toISOString() };
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
