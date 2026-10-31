# Rivolt Pro launch: copy that is not in a page file

The public copy for the Pro launch that lives outside the web content, README and
App Store listing: the launch notice, the in-app banner, the end-of-year notice,
the demo clip end cards, the forum signature and the GitHub About text. The pages
themselves are in this same pull request. Placeholders: `LAUNCH_DATE`,
`FOUNDING_END_DATE` (launch + 1 year), `TERMS_EFFECTIVE_EXISTING` (launch + 30
days). Nothing here is sent or posted by CI; the owner sends and posts.

Rules that apply to every line: "Unofficial, not affiliated with Rivian" on every
pricing surface; never "free forever", "always free" (except the export, which
is), "limited beta", "invite-only" or "early access"; one price everywhere; no
figures that are not on the support page.

## 1. Launch notice (email; the Terms section 11 notice)

Sent once, to every verified address, including accounts with engagement emails
off, as a service notice. Transactional, so no unsubscribe link is required, but
keep the standard footer.

Subject: Rivolt Pro, and what it means for your account

Hi,

Rivolt has been free during its public beta. From LAUNCH_DATE the hosted service
at rivolt.dev has a free tier and a paid plan, Rivolt Pro.

**Your account has Pro at no charge until FOUNDING_END_DATE.** There is no card
on file and nothing will be charged. We will email you and show a notice in the
app at least 30 days before that date. After it, your account is on the free tier
unless you choose to subscribe.

**What the free tier keeps:** every drive and charge recorded, on every connected
vehicle; Now, Activity, Money and Truck for the last 90 days; the monthly
statement; a single-day trip planner; three saved trips; share cards; imports;
every alert; and the full export of everything Rivolt holds, always, including
history older than 90 days. Nothing is ever deleted when a plan ends.

**What Pro is:** unlimited history and trends, range near a temperature,
multi-day trip plans with hotel and campground overnights, re-plan from where the
truck is, plan-vs-actual, unlimited saved trips, every vehicle shown, and the
Lock Screen widgets and charging Live Activity on iPhone. $3.99 a month or
$34.99 a year, the same on the web and in the app.

**The Terms and the Privacy Policy change with this.** The updated Terms
(section 5, free tier and Pro, and the refund line in section 2) take effect for
your account on TERMS_EFFECTIVE_EXISTING, 30 days from this email:
https://rivolt.dev/terms. The Privacy Policy gains an "If you buy Rivolt Pro"
section that applies only to accounts that subscribe: https://rivolt.dev/privacy.
Until TERMS_EFFECTIVE_EXISTING the previous Terms apply: https://rivolt.dev/terms/2026-10-23.
If you do not agree, you can export and delete your account from Settings before
then.

Nothing to do today. Questions: support@rivolt.dev.

The Rivolt team
Rivolt is unofficial and not affiliated with Rivian.

## 2. In-app notice (banner on Now and a row in Settings, dismissable; shown until TERMS_EFFECTIVE_EXISTING)

Title: Rivolt now has a free tier and Pro

Body: Your account has Pro at no charge until FOUNDING_END_DATE, with no card and
nothing charged. The updated Terms take effect for you on
TERMS_EFFECTIVE_EXISTING. Nothing is deleted when a plan ends, and the full export
is always free.

Buttons: "What changes" (→ /support#free-and-pro) · "Read the Terms" (→ /terms) ·
"Got it"

## 3. New sign-up and trial screens (web)

Sign-up page line, web (replaces "Free during the beta"): "Free to start. Every
new account gets 14 days of Rivolt Pro; no card, it ends on its own and nothing is
charged." (The iPhone sign-up does not promise "no card": its trial is Apple's
introductory offer, described on the paywall.)

Trial card on Now, day 1: "You have Pro for 14 days: widgets, multi-day plans,
full history. No card, nothing charges. Afterwards the free tier keeps every
recording and the full export."

Trial card, last 3 days: "Pro ends in {n} days. Nothing is deleted either way.
Keep Pro for $3.99 a month or $34.99 a year, or do nothing."

Paywall (web, Settings → Plan; the Apple one follows Apple's 3.1.2 wording):
"Rivolt Pro · $3.99 a month or $34.99 a year · Renews automatically until you
cancel · Cancel any time in one step from this page · 14-day refund on the first
charge, no questions · Stop paying and nothing is deleted · Payment through
Stripe; Rivolt never sees your card · Terms · Privacy". Checkout button:
"Subscribe: $34.99 a year, renews yearly" / "Subscribe: $3.99 a month, renews
monthly". No pre-ticked boxes.

## 4. End-of-year notice (email and in-app, at least 30 days before FOUNDING_END_DATE; a dated task)

Subject: Your free year of Rivolt Pro ends on FOUNDING_END_DATE

Hi,

Your account has had Rivolt Pro at no charge since Pro launched. That ends on
FOUNDING_END_DATE. Nothing is charged and nothing is deleted.

**What changes on that date:** Activity, Money and Truck show the last 90 days;
the planner plans single-day trips; three saved trips stay editable and the rest
stay read-only; one vehicle is shown at a time; trends and range near a
temperature are not shown; the iPhone widgets and the charging Live Activity
show the free state.

**What does not change:** every drive and charge keeps being recorded on every
connected vehicle, every alert keeps working, and the full export of everything,
including history older than 90 days, stays free. Export it first if you like:
Settings → Data.

**To keep Pro:** $3.99 a month or $34.99 a year, in Settings → Plan or in the
iPhone app. Or do nothing.

The Rivolt team
support@rivolt.dev
Rivolt is unofficial and not affiliated with Rivian.

## 5. Demo clip end cards (re-encode on launch day)

Last frame of every clip: "rivolt.dev · open source · free tier and Pro.
Unofficial, not affiliated with Rivian. Sample truck, generated data."

Trip clip only, under the first caption: "Shown with Rivolt Pro (multi-day
plans). The single-day planner is free."

Clips posted before launch keep "free during the beta", which is true when
posted.

## 6. Forum signature and profile line (the owner's call; the owner posts)

"Tracking my R1S with Rivolt, open source, free tier and Pro: rivolt.dev"

## 7. GitHub About (repository description; the owner sets it)

"Unofficial, open-source companion app for Rivian owners: drives, charging
costs, trips, efficiency and lease mileage. Free tier and Pro; self-hostable."

## 8. Social bios and directory tagline (replace "free during beta" on launch day)

- TikTok (≤80): "Open-source Rivian companion, free tier and Pro. Unofficial.
  Sample truck shown." (79)
- Instagram (≤150): "Open-source Rivian companion: charging cost, trips, lease
  miles. Free tier and Pro. Unofficial, not affiliated with Rivian. Sample truck
  shown." (143)
- Directory tagline (≤80): "Open-source Rivian tracker, free tier and Pro:
  charging cost, trips, lease miles." (78)

## 9. Statement email, last month of the free year (one line, existing send)

"Your free year of Pro ends on FOUNDING_END_DATE. Nothing is deleted; keep Pro
from Settings → Plan, or do nothing."

## 10. Paywall, plan card and lapse state (for ux, Pro 9)

Title: **Rivolt Pro**
Subtitle: Full history, multi-day trips and widgets, from $3.99 a month.

What's included (one list, as shown):
- Unlimited history and trends (free: the last 90 days)
- Range near a temperature
- Multi-day trip plans with hotel and campground overnights
- Re-plan from where the truck is, and plan-vs-actual after the trip
- Unlimited saved trips (free: 3)
- Every vehicle shown (free: 1)
- iPhone: Lock Screen and Home Screen widgets, the charging Live Activity
- Always free, on every plan: recording, every alert, imports, share cards, the full export, deletion

Plan choices (two options, nothing pre-selected):
- Yearly: $34.99 a year (about $2.92 a month)
- Monthly: $3.99 a month

Trial line, web (trial granted at sign-up, no card): "You have 14 days of Pro free, with no card. It ends on {trial_end} on its own; nothing is charged unless you subscribe."
Trial line, web, after the trial: "14 days of Pro were free when you signed up. Subscribe to keep it; cancel any time."
Trial line, iPhone (Apple introductory offer): "14 days free, then $34.99 a year (or $3.99 a month). Renews automatically until cancelled. Cancel at least 24 hours before the end of the trial or any period in Settings → Apple ID → Subscriptions. Payment is charged to your Apple ID."

Web consent line, directly above the checkout button, with an unticked box: "I understand that Rivolt Pro renews automatically at $34.99 a year [or $3.99 a month] until I cancel, that the first charge is {first_charge}, and that I can cancel in one step from Settings → Plan. 14-day refund on the first charge, no questions. Terms · Privacy"
Rule for {first_charge} (lead's decision, DA's rule): a subscription bought during the trial starts billing when the trial ends, so the remaining trial days are not lost. The phrase is generated from the actual Stripe Checkout session, "on {trial_end}" when the session has a trial end, "today" when there is no trial left; never hard-coded, with a test that the line and the session agree.
Checkout button: "Subscribe: $34.99 a year, renews yearly" / "Subscribe: $3.99 a month, renews monthly"
Under the button: "Payment through Stripe. Rivolt never sees your card. Stop paying and nothing is deleted."
iPhone, under the Subscribe button: "Terms · Privacy" (links to rivolt.dev/terms and rivolt.dev/privacy; Apple row A4), then "Payment is charged to your Apple ID. Stop paying and nothing is deleted."

Restore purchases (iPhone): label "Restore purchases"; helper "Bought Pro on this Apple ID before? Restore it here. A subscription bought on rivolt.dev is already on your account; sign in and it applies."
Manage subscription: label "Manage subscription"; helper, iPhone: "Opens your Apple subscriptions, where you change or cancel the plan. Cancelling keeps Pro until the paid period ends."; web: "Opens Stripe's portal, where you change the plan, update the card or cancel in one step. Cancelling keeps Pro until the paid period ends."

Lapse banner (dismissable, Now and Settings): "Your Pro ended on {date}. Showing the last 90 days. Nothing is deleted; older history stays stored and is in your export any time (Settings → Data)." Buttons: "Keep Pro" · "Export everything" · "Dismiss"
Payment-failed banner (7-day grace, web only): "A payment for Rivolt Pro failed. Update your card in Settings → Plan by {grace_end} to keep Pro. Either way, nothing is deleted."

Settings plan card states (title, line, action):
- Free: "Free" · "Recording, alerts, the last 90 days and the full export, always." · "See Pro"
- Trial: "Pro trial, ends {date}" · "No card, nothing is charged. Afterwards the free tier keeps every recording and the full export." · "Keep Pro"
- Pro via Apple: "Rivolt Pro, renews {date}" · "Billed through Apple." · "Manage subscription"
- Pro via Stripe: "Rivolt Pro, renews {date}" · "Billed through Stripe, $34.99 a year [or $3.99 a month]." · "Manage subscription"
- Pro, cancelled but still paid up: "Rivolt Pro until {date}" · "Cancelled; Pro stays on until then. Nothing is deleted after." · "Resubscribe"
- Founding year: "Rivolt Pro, free until {date}" · "Yours at no charge for the first year; no card, nothing is charged. We'll email you 30 days before it ends." · "See what free keeps"
- Grace: "Rivolt Pro, payment failed" · "Update your card by {grace_end} to keep Pro. Nothing is deleted either way." · "Update card"
- Self-hosted (billing off): "Rivolt Pro" · "Everything is on; this server has billing off." · none

Every sheet ends with: "Unofficial, not affiliated with Rivian."
