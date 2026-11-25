# Rivolt Pro subscriptions: desired state in App Store Connect

Read by `asc_subscriptions.py` (plan only for now; apply is switched on by its own PR after the owner approves).
Format: `## Section`, then `Key: value` lines. Product ids and the subscription period cannot be changed once created.
Prices are the web prices ($3.99 a month, $34.99 a year), United States storefront only, matching listing.md and the Terms.

## Group
Reference name: Rivolt Pro
Display name: Rivolt Pro

## Notifications
Version: V2
Production URL: https://rivolt.dev/api/billing/apple/notifications
Sandbox URL: https://preview.rivolt.dev/api/billing/apple/notifications

## Yearly
Product ID: dev.rivolt.app.pro.yearly
Reference name: Rivolt Pro Yearly
Period: ONE_YEAR
Level: 1
Price: 34.99
Display name: Rivolt Pro Yearly
Description: Full history, trends and multi-day trips

## Monthly
Product ID: dev.rivolt.app.pro.monthly
Reference name: Rivolt Pro Monthly
Period: ONE_MONTH
Level: 2
Price: 3.99
Display name: Rivolt Pro Monthly
Description: Full history, trends and multi-day trips

## Review
Notes: Rivolt Pro unlocks unlimited history and trends, multi-day trip plans, every vehicle and the iPhone widgets. To see the purchase screen without a Rivian account, tap "Try the demo truck" on the sign-in screen, then open Settings and tap Rivolt Pro. The subscription is also usable on rivolt.dev; the app applies the Pro tier of the signed-in Rivolt account. Cancel any time in Settings, Apple ID, Subscriptions; cancelling keeps Pro until the paid period ends and nothing is deleted. Available in the United States storefront only.
