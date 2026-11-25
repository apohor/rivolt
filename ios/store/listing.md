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
Free tier and Pro tier. See your Rivian's drives, each charge's cost and its source, tire pressures and parked drain. Open source. Unofficial.

## Description
Rivolt is an open-source companion for Rivian owners. It records your truck's drives and charges, prices each charge, using what Rivian billed where it billed, otherwise your rate or a labelled estimate, and shows where each figure came from.

ON YOUR IPHONE
• Now: charge level, range, what the truck is doing and how fresh that reading is, with a switcher if you have more than one truck.
• Activity and Money: every drive with its route and charts, every charge with kWh, cost and the source of the price, and a monthly statement.
• Truck: tire pressures with a plain "Low" below 32 psi or when the truck says so, and parked drain.

HOW IT WORKS
Sign in with your Rivolt account through a browser sheet. The app never sees your Rivian password. It keeps only a Rivolt sign-in token, which you can revoke from Signed-in devices. We recommend connecting a second Rivian account added as an Authorized Driver, so you can remove Rivolt from the Rivian app at any time. Rivolt sends no commands to your truck (no locks, climate or charging controls). Reading its data can occasionally wake it.

FREE AND PRO
Rivolt has a free tier and a Pro tier. The free tier records every drive and charge and always includes the full export of your data. Rivolt Pro adds unlimited history and trends, multi-day trip plans with overnights, plan-vs-actual, every vehicle. Pro belongs to your Rivolt account: sign in to the app with an account that has Pro and it applies. The app itself sells nothing. If a Pro subscription ends, nothing is deleted and your export stays free.

Estimates are labelled as estimates. Nothing compares your truck with other owners'. There are no ads and no third-party analytics; you can export or delete your data from Settings. The code is open source (MIT) and you can run your own server.

Rivolt is an independent project. It is not affiliated with, endorsed by or sponsored by Rivian. It uses Rivian's unofficial API, so it can break when Rivian changes things.

Terms: https://rivolt.dev/terms · Privacy: https://rivolt.dev/privacy

## Keywords
EV,electric truck,charging cost,trip,efficiency,lease,R1T,R1S,R2,range,open source

## Support URL
https://rivolt.dev/support

## Marketing URL
https://rivolt.dev

## Privacy Policy URL
https://rivolt.dev/privacy

## What's New
First App Store release. Now shows what your Rivian is doing: a 24-hour battery graph while parked, a drive graph while driving, and a charge graph with time left while charging. Activity and Money list every drive and charge with its cost and where the price came from, and a drive can be shared as a card. Plan a trip with charging stops, save it, and see each leg on the map. Truck shows tire pressures and parked drain. No account? Try the demo truck on the sign-in screen.

## Review notes
Rivolt is an unofficial companion for Rivian vehicle owners. Tap "Try the demo truck" on the sign-in screen; no account needed (a sample truck with generated data), and it shows Now, Activity, Money, sample trip plans (multi-day plans with overnights are a Pro-tier feature) and Truck. Widgets, push notifications and the charging Live Activity need a real account and a truck, so they are not in the demo. The app sends no commands to vehicles. Sign-in for real owners opens a browser sheet to rivolt.dev.

Pricing: the app has no in-app purchase and no purchase button, and links to no payment page. Rivolt has a free tier and a Pro tier; Pro is a subscription bought on rivolt.dev (the web), and the app applies it to the signed-in Rivolt account. Account deletion starts in Settings and finishes on rivolt.dev, where the person confirms. We are available in the United States storefront only, matching our Terms (adults in the United States).

No sign-in account is needed for review: a new account without a Rivian only shows "Connect your Rivian", so the demo truck is the review path.

## Screenshot captions
Six shots at 6.9" and one smaller Dynamic Island size (6.3"), dark primary, one light Now variant if a slot is free; the caption sits at the top, with a small footer "Sample truck, generated data". Demo only: generated data, no real accounts, no Rivian logo, colours or vehicle photos.
1. Your Rivian at a glance
2. What charging actually cost
3. Every drive, route and charts
4. Every charge, with its cost and where it came from
5. Tires, sleep and health, from your truck
6. Sends no commands to your truck. Your Rivian password is never stored.

## Other settings
Age rating 4+. Category Utilities, secondary Travel. "Rivian", "R1T", "R1S" and "R2" appear only descriptively. If review objects to "Rivian" in the name, use the fallback name and keep Rivian in the subtitle or description only. The privacy nutrition label is in the readiness notes the lead keeps with the owner's App Store Connect checklist; while the app sells nothing, the label has no **Purchases** row; the submission that adds the in-app subscription adds it (purchase history, linked to the user, used for app functionality, not for tracking), matching the "If you buy Rivolt Pro" section of the privacy policy. This listing describes the product as it is: a free tier and a Pro tier, with Pro bought on the web and no purchase inside the iPhone app. If iPhone billing is ever turned on, the Pro paragraph, What's New and the review notes change in the same PR.
