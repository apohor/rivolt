# Rivolt on the App Store: listing text

The text App Store Connect asks for, kept here so a change is one reviewed PR (growth writes it, DA checks it, the owner pastes it into App Store Connect; **nothing here is submitted by CI**). `check_listing.py` runs in the iOS workflow and fails when a field is over Apple's limit, when the review notes mention a password or hold an email address (this repo is public: demo or review account details go only in App Store Connect's private field), or when a screenshot caption uses a banned word.

Each `##` heading below is one field; its text runs to the next heading.

## Name
Rivolt for Rivian owners

## Name fallback
Rivolt: EV drives and charging

## Subtitle
Drives, charging cost, truck

## Promotional text
Free to start, open source. See your Rivian's drives, each charge's cost and its source, tire pressures and sleep health. Rivolt Pro adds widgets and more. Unofficial.

## Description
Rivolt is an open-source companion for Rivian owners. It records your truck's drives and charges, prices each charge, using what Rivian billed where it billed, otherwise your rate or a labelled estimate, and shows where each figure came from.

ON YOUR IPHONE
• Now: charge level, range, what the truck is doing and how fresh that reading is, with a switcher if you have more than one truck.
• Widgets (Rivolt Pro): charge and range on the Home Screen and the Lock Screen. A widget that can't refresh says how old its numbers are instead of looking current.
• Notifications: charging complete, software updates, and, if you turn them on, a plug-in reminder, milestones and an alert when a door, the frunk, the liftgate, the tailgate or the tonneau has been left open for 15 minutes after parking.
• Activity and Money: every drive with its route and charts, every charge with kWh, cost and the source of the price, and a monthly statement.
• Truck: tire pressures with a plain "Low" below 32 psi or when the truck says so, parked drain, pack capacity, and sleep health compared only with your own truck's last 30 days.

HOW IT WORKS
Sign in with your Rivolt account through a browser sheet on rivolt.dev. The app never sees your Rivian password. It keeps only a Rivolt sign-in token, which you can revoke from Signed-in devices. We recommend connecting a second Rivian account added as an Authorized Driver, so you can remove Rivolt from the Rivian app at any time. Rivolt sends no commands to your truck (no locks, climate or charging controls). Reading its data can occasionally wake it.

FREE AND PRO
The free tier records every drive and charge, shows the last 90 days, sends every alert, and always includes the full export of your data. Rivolt Pro is a subscription, $3.99 a month or $34.99 a year, that adds unlimited history and trends, multi-day trip plans with overnights, plan-vs-actual, every vehicle, and the Lock Screen widgets and charging Live Activity. Subscriptions renew automatically unless cancelled at least 24 hours before the end of the period; manage or cancel them in your Apple subscriptions. If you stop paying, nothing is deleted and your export stays free.

Estimates are labelled as estimates. Nothing compares your truck with other owners'. There are no ads and no third-party analytics; you can export or delete your data from Settings on rivolt.dev. The code is open source (MIT) and you can run your own server.

Rivolt is an independent project. It is not affiliated with, endorsed by or sponsored by Rivian. It uses Rivian's unofficial API, so it can break when Rivian changes things.

Terms: https://rivolt.dev/terms · Privacy: https://rivolt.dev/privacy

## Keywords
EV,electric truck,charging cost,trip,efficiency,lease,R1T,R1S,R2,range,widget,open source

## Support URL
https://rivolt.dev/support

## Marketing URL
https://rivolt.dev

## Privacy Policy URL
https://rivolt.dev/privacy

## What's New
First App Store release: Now, notifications, Activity, Money and Truck, signed in with your Rivolt account. Rivolt Pro, with widgets and the charging Live Activity, as an in-app subscription.

## Review notes
Rivolt is an unofficial companion for Rivian vehicle owners. Tap "Try the demo truck" on the sign-in screen; no account needed (a sample truck with generated data), and every feature, including the Pro ones, is browsable there. The app sends no commands to vehicles. Sign-in for real owners opens a browser sheet to rivolt.dev.

In-app purchase: one auto-renewable subscription group, "Rivolt Pro", with a monthly ($3.99) and a yearly ($34.99) product, no introductory offer. It unlocks the Home Screen and Lock Screen widgets, the charging Live Activity, unlimited history and trends, and multi-day trip planning. The paywall (Settings → Account → Plan) shows the price, period, auto-renewal and cancellation terms, links to the Terms and Privacy Policy, and has Restore Purchases and Manage Subscription. A web subscription bought on rivolt.dev is also honoured in the app, and the app never links out to buy. The entitlement is verified server-side from the App Store Server API; sandbox purchases work on the test account.

A plain test account is also supplied in App Store Connect's private field; it has no truck connected, because a Rivian is needed for your own data.

## Screenshot captions
Six shots at 6.9" and 6.5", dark primary, one light Now variant if a slot is free; the caption sits at the top, with a small footer "Sample truck, generated data". Demo only: generated data, no real accounts, no Rivian logo, colours or vehicle photos.
1. Your Rivian at a glance
2. What charging actually cost
3. Every drive, route and charts
4. Every charge, with its cost and where it came from
5. Tires, sleep and health, from your truck
6. Sends no commands to your truck. Your Rivian password is never stored.

## Other settings
Age rating 4+. Category Utilities, secondary Travel. "Rivian", "R1T", "R1S" and "R2" appear only descriptively. If review objects to "Rivian" in the name, use the fallback name and keep Rivian in the subtitle or description only. The privacy nutrition label is in the readiness notes the lead keeps with the owner's App Store Connect checklist; the submission that adds the subscription also adds **Purchases** (purchase history, linked to the user, used for app functionality, not for tracking), matching the "If you buy Rivolt Pro" section of the privacy policy. The listing with "Free during the beta" and the review note "Nothing is purchased in the app" must not outlive that submission.
