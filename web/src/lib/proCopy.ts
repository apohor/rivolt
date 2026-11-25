// Pro screen wording, from growth's docs/PRO_LAUNCH_COPY.md section 10 (PR #1366; DA's copy check is part of that PR, so until it
// lands this is LAUNCH-BLOCKING to re-verify against the merged text). Prices are the plan's. The iPhone twin is ProCopy in
// ios/Rivolt/Views/ProPaywall.swift.

import { planDate, type PlanState } from "./plan";

export type Interval = "month" | "year";

// How far back the free window reaches, in words: the number is the server's (history_months of /api/auth/me; 6, or 12 for an account still
// inside the 30 days' notice of the change), never written here. Without it (an older server) nothing is claimed about the length.
export function windowWords(months?: number | null): string {
  return typeof months === "number" && months > 0 ? `the last ${months} months` : "only recent months";
}

// The Pro sheet's list: the history line names the window of this account's own free tier.
export function proIncluded(months?: number | null): string[] {
  return [`Unlimited history and trends (free tier: ${windowWords(months)})`, ...PRO.included];
}

export const PRO = {
  title: "Rivolt Pro",
  subtitle: "Full history, multi-day trips and more widgets, from $3.99 a month.",
  included: [
    "Range near a temperature",
    "Multi-day trip plans with hotel and campground overnights",
    "Rain or snow likely at your stops on arrival (contiguous US, NOAA RRFS forecast radar; free: a note when stops are wet)",
    "Re-plan from where the truck is, and plan-vs-actual after the trip",
    "Unlimited saved trips (free: 3)",
    "Every vehicle shown (free: 1)",
    "iPhone: every widget except the small Now widget (the charging Live Activity is free)",
    "Always free, on every plan: recording, every alert, imports, share cards, trip share links, the charging Live Activity, the small Now widget, the full export, deletion",
  ],
  // Nothing is pre-selected.
  options: {
    year: { label: "Yearly", price: "$34.99", unit: "a year", extra: "about $2.92 a month", renews: "yearly" },
    month: { label: "Monthly", price: "$3.99", unit: "a month", extra: "", renews: "monthly" },
  },
  underButton: "Payment through Stripe. Rivolt never sees your card. Stop paying and nothing is deleted.",
  // Stripe Checkout is US-only at launch (DA E1). Web only: the iPhone sheet leaves territories to Apple.
  usBilling: "Web purchases need a US billing address for now.",
  manageStripe: "Manage subscription",
  manageStripeHelp: "Opens Stripe's portal, where you change the plan, update the card or cancel in one step. Cancelling keeps Pro until the paid period ends.",
  manageApple: "Opens your Apple subscriptions, where you change or cancel the plan. Cancelling keeps Pro until the paid period ends. On your iPhone: Settings → your name → Subscriptions.",
  unofficial: "Unofficial, not affiliated with Rivian.",
  billingUnavailable: "Billing isn't available yet. Nothing was charged.",
} as const;

// Self-serve refund (web Pro, within 14 days of a payment, once per account). Facts only; no promises about when the bank shows it.
export function money(cents: number, currency: string): string {
  try {
    return new Intl.NumberFormat("en-US", { style: "currency", currency: currency.toUpperCase() }).format(cents / 100);
  } catch {
    return `${(cents / 100).toFixed(2)} ${currency.toUpperCase()}`;
  }
}
export const REFUND = {
  action: "Refund and cancel",
  offer: (amount: string, until: string) => `You can refund your latest payment of ${amount} until ${until}. Pro ends right away and the account goes back to the free tier. Nothing is deleted.`,
  confirm: (amount: string) => `Refund ${amount} and end Pro now? This can be done once per account.`,
  yes: "Yes, refund and cancel",
  keep: "Keep Pro",
  done: (amount: string) => `Refunded ${amount} to the card you paid with. Pro has ended and the account is on the free tier. Nothing was deleted.`,
  gone: "A refund is no longer available for this account.",
  failed: "We couldn't refund right now. Try again, or email support@rivolt.dev.",
} as const;

export function checkoutLabel(i: Interval): string {
  const o = PRO.options[i === "year" ? "year" : "month"];
  return `Subscribe: ${o.price} ${o.unit}, renews ${o.renews}`;
}

// Where the first charge happens, for the consent line: "today", except for an account inside free Pro (the founding year, or a grant
// from the operator) subscribing early, whose billing starts when the free period ends (the lead's decision, so no free day is lost). TODO(Pro 7): take this from the actual Stripe Checkout
// session ("on {period_end}" when the session carries a billing start, "today" otherwise) instead of the account's founding end.
export function firstCharge(state: PlanState): string {
  if (state.kind === "pro" && (state.source === "founding" || state.source === "grant")) {
    const d = planDate(state.periodEnd);
    if (d) return `on ${d}`;
  }
  return "today";
}

// The renewal sentence for the choice, directly above the button next to an unticked box (compliance B1/B2).
export function consentText(i: Interval, state: PlanState): string {
  const o = PRO.options[i === "year" ? "year" : "month"];
  return `I understand that Rivolt Pro renews automatically at ${o.price} ${o.unit} until I cancel, that the first charge is ${firstCharge(state)}, and that I can cancel in one step from Settings → Account → Plan. 14-day refund on any payment, no questions.`;
}

// The Settings plan card, one state at a time: [title, line, action | null].
export function planCardCopy(state: PlanState): { title: string; line: string; action: string | null } | null {
  switch (state.kind) {
    case "off":
      return null;
    case "free":
      return { title: "Free", line: `Recording, alerts, ${windowWords(state.months)} and the full export, always.`, action: "See Pro" };
    case "pro":
      // A failed payment comes first: it is the one that needs the person to do something (web only: Apple handles its own).
      if (state.graceEnds && state.source === "stripe")
        return { title: "Rivolt Pro, payment failed", line: `Update your card by ${planDate(state.graceEnds) || "the end of the week"} to keep Pro. Nothing is deleted either way.`, action: "Update card" };
      // A subscription set to not renew must not say it renews.
      if (state.cancelsAt && (state.source === "stripe" || state.source === "apple"))
        return {
          title: `Rivolt Pro until ${planDate(state.cancelsAt) || planDate(state.periodEnd) || "the end of the period"}`,
          line: "Cancelled; Pro stays on until then. Nothing is deleted after.",
          action: state.source === "stripe" ? "Resubscribe" : "Manage subscription",
        };
      if (state.source === "apple") return { title: `Rivolt Pro${state.periodEnd ? `, renews ${planDate(state.periodEnd)}` : ""}`, line: "Billed through Apple.", action: "Manage subscription" };
      if (state.source === "stripe") return { title: `Rivolt Pro${state.periodEnd ? `, renews ${planDate(state.periodEnd)}` : ""}`, line: "Billed through Stripe.", action: "Manage subscription" };
      if (state.source === "founding")
        return {
          title: `Rivolt Pro, free until ${planDate(state.periodEnd) || "the end of the year"}`,
          line: "Yours at no charge for the first year; no card, nothing is charged. We'll email you at least 30 days before it ends.",
          action: "See what free keeps",
        };
      // A grant from the operator: free until its date, and no email comes before it ends (the founding line's promise is not made here).
      if (state.periodEnd && planDate(state.periodEnd))
        return { title: `Rivolt Pro, free until ${planDate(state.periodEnd)}`, line: "Yours at no charge until then; no card, nothing is charged. Nothing is deleted when it ends.", action: null };
      return { title: "Rivolt Pro", line: "Granted to this account.", action: null };
    case "lapsed":
      return {
        title: `Your Pro ended${planDate(state.endedOn) ? ` on ${planDate(state.endedOn)}` : ""}`,
        line: `Showing ${windowWords(state.months)}. Nothing is deleted; older history stays stored and is in your export any time (Settings → Data).`,
        action: "Keep Pro",
      };
  }
}

// The payment-failed banner (7-day grace, web only): shown while Stripe is retrying; nothing is deleted either way.
export function graceBanner(state: PlanState): string | null {
  if (state.kind !== "pro" || state.source !== "stripe" || !state.graceEnds) return null;
  return `A payment for Rivolt Pro failed. Update your card in Settings → Account → Plan by ${planDate(state.graceEnds) || "the end of the week"} to keep Pro. Either way, nothing is deleted.`;
}

// The lapse banner (dismissable, on Now and in Settings).
export function lapseBanner(state: PlanState): string {
  const on = state.kind === "lapsed" ? planDate(state.endedOn) : "";
  return `Your Pro ended${on ? ` on ${on}` : ""}. Showing ${windowWords(state.kind === "lapsed" ? state.months : undefined)}. Nothing is deleted; older history stays stored and is in your export any time (Settings → Data).`;
}

// Shown in the delete-account flow of an account paying through the App Store: the server cannot cancel an Apple subscription, so the
// person must (the iPhone's delete sheet says the same).
export const DELETE_APPLE_NOTE = "Cancel your App Store subscription first in iPhone Settings → Subscriptions; deleting your Rivolt account doesn't cancel it.";
