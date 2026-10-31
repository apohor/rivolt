// Pro screen wording, from growth's docs/PRO_LAUNCH_COPY.md section 10 (PR #1366; DA's copy check is part of that PR, so until it
// lands this is LAUNCH-BLOCKING to re-verify against the merged text). Prices are the plan's. The iPhone twin is ProCopy in
// ios/Rivolt/Views/ProPaywall.swift.

import { planDate, type PlanState } from "./plan";

export type Interval = "month" | "year";

export const PRO = {
  title: "Rivolt Pro",
  subtitle: "Full history, multi-day trips and widgets, from $3.99 a month.",
  included: [
    "Unlimited history and trends (free: the last 90 days)",
    "Range near a temperature",
    "Multi-day trip plans with hotel and campground overnights",
    "Re-plan from where the truck is, and plan-vs-actual after the trip",
    "Unlimited saved trips (free: 3)",
    "Every vehicle shown (free: 1)",
    "iPhone: Lock Screen and Home Screen widgets, the charging Live Activity",
    "Always free, on every plan: recording, every alert, imports, share cards, the full export, deletion",
  ],
  // Nothing is pre-selected.
  options: {
    year: { label: "Yearly", price: "$34.99", unit: "a year", extra: "about $2.92 a month", renews: "yearly" },
    month: { label: "Monthly", price: "$3.99", unit: "a month", extra: "", renews: "monthly" },
  },
  underButton: "Payment through Stripe. Rivolt never sees your card. Stop paying and nothing is deleted.",
  manageStripe: "Manage subscription",
  manageStripeHelp: "Opens Stripe's portal, where you change the plan, update the card or cancel in one step. Cancelling keeps Pro until the paid period ends.",
  manageApple: "Opens your Apple subscriptions, where you change or cancel the plan. Cancelling keeps Pro until the paid period ends. On your iPhone: Settings → your name → Subscriptions.",
  unofficial: "Unofficial, not affiliated with Rivian.",
  billingUnavailable: "Billing isn't available yet. Nothing was charged.",
} as const;

export function checkoutLabel(i: Interval): string {
  const o = PRO.options[i === "year" ? "year" : "month"];
  return `Subscribe: ${o.price} ${o.unit}, renews ${o.renews}`;
}

// Where the first charge happens, for the consent line: when a trial is running, billing starts when it ends, so no trial day is lost
// (the lead's decision); otherwise today. TODO(Pro 7): take this from the actual Stripe Checkout session ("on {trial_end}" when the
// session carries a trial end, "today" when none is left) instead of the account's trial end.
export function firstCharge(state: PlanState): string {
  if (state.kind === "trial") {
    const d = planDate(state.ends);
    if (d) return `on ${d}`;
  }
  return "today";
}

// The renewal sentence for the choice, directly above the button next to an unticked box (compliance B1/B2).
export function consentText(i: Interval, state: PlanState): string {
  const o = PRO.options[i === "year" ? "year" : "month"];
  return `I understand that Rivolt Pro renews automatically at ${o.price} ${o.unit} until I cancel, that the first charge is ${firstCharge(state)}, and that I can cancel in one step from Settings → Account → Plan. 14-day refund on the first charge, no questions.`;
}

// The trial line for the web (the trial is granted at sign-up, with no card).
export function trialLine(state: PlanState): string | null {
  if (state.kind === "trial") {
    const d = planDate(state.ends);
    return `You have 14 days of Pro free, with no card. It ends${d ? ` on ${d}` : ""} on its own; nothing is charged unless you subscribe.`;
  }
  if (state.kind === "lapsed" && state.endedSource === "trial") return "14 days of Pro were free when you signed up. Subscribe to keep it; cancel any time.";
  return null;
}

// The Settings plan card, one state at a time: [title, line, action | null].
export function planCardCopy(state: PlanState, daysLeftText?: string): { title: string; line: string; action: string | null } | null {
  switch (state.kind) {
    case "off":
      return null;
    case "free":
      return { title: "Free", line: "Recording, alerts, the last 90 days and the full export, always.", action: "See Pro" };
    case "trial":
      return {
        title: `Pro trial, ends ${planDate(state.ends) || "soon"}`,
        line: `${daysLeftText ? `${daysLeftText[0].toUpperCase()}${daysLeftText.slice(1)}. ` : ""}No card, nothing is charged. Afterwards the free tier keeps every recording and the full export.`,
        action: "Keep Pro",
      };
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
          line: "Yours at no charge for the first year; no card, nothing is charged. We'll email you 30 days before it ends.",
          action: "See what free keeps",
        };
      return { title: "Rivolt Pro", line: "Granted to this account.", action: null };
    case "lapsed":
      return {
        title: `Your Pro ended${planDate(state.endedOn) ? ` on ${planDate(state.endedOn)}` : ""}`,
        line: "Showing the last 90 days. Nothing is deleted; older history stays stored and is in your export any time (Settings → Data).",
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
  return `Your Pro ended${on ? ` on ${on}` : ""}. Showing the last 90 days. Nothing is deleted; older history stays stored and is in your export any time (Settings → Data).`;
}
