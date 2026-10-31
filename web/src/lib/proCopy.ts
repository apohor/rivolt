// Pro screen wording, in one place. PLACEHOLDER until growth's text lands (pm: public copy comes from growth's draft); the prices
// are the plan's ($3.99 a month, $34.99 a year, 14-day trial). Swapping the words is a change to this file only.

export const PRO = {
  title: "Rivolt Pro",
  subtitle: "Everything in Rivolt, for as long as you want it.",
  monthly: { label: "Monthly", price: "$3.99", period: "month" },
  yearly: { label: "Yearly", price: "$34.99", period: "year" },
  included: [
    "Unlimited history in Now, Activity and Truck (free shows the last 90 days)",
    "Every month of Money, year views and off-peak savings over any period",
    "Trends, and range near a temperature",
    "Multi-day trips with hotel and campground overnights, re-plan from here, plan vs actual, unlimited saved trips",
    "Every vehicle shown",
    "Lock Screen and Home Screen widgets and the charging Live Activity on iPhone",
  ],
  keepsFree: "Free keeps: recording, your full export, alerts, imports, and a single-day planner. Nothing is ever deleted when Pro ends.",
  webTrialNoCard: "Your 14-day trial needs no card and never charges you.",
  // Next to the checkout button (B1/B2): renewal, price, first charge and how to cancel, with an explicit consent.
  consent: "I understand this renews automatically every {period} at {price} until I cancel, and I can cancel any time under Settings → Account → Plan.",
  checkoutButton: "Subscribe",
  restore: "Restore purchases",
  manageStripe: "Manage subscription",
  manageApple: "Manage in the App Store: Settings → your name → Subscriptions on your iPhone.",
  terms: "Terms",
  privacy: "Privacy",
} as const;
