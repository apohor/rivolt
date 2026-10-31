# Support

Rivolt is run by ZeroDowntime LLC. Email [support@rivolt.dev](mailto:support@rivolt.dev); we aim to reply within a few days. Requests about your data are handled within 30 days, as the [Privacy Policy](/privacy) says. Include the page or screen you were on, never your password.

Bug reports and feature requests are also welcome on [GitHub](https://github.com/apohor/rivolt/issues). Issues are public, so leave out your VIN, address, location and email address.

## Common questions

**What is Rivolt?**
An open-source companion for Rivian owners with a free tier and a paid Rivolt Pro plan. It records your truck's drives and charges, prices each charge, using what Rivian billed where it billed, otherwise your rate or a labelled estimate, and shows where each figure came from. It works on the web at rivolt.dev and as an iPhone app.

**Is it official?**
No. Rivolt is independent and is not affiliated with, endorsed by or sponsored by Rivian. It reads data through Rivian's unofficial API, which Rivian can change or block at any time, so Rivolt can break.

**How do I connect my truck?**
Sign in to rivolt.dev and add your Rivian account on the Connect step. We recommend adding a second Rivian account as an Authorized Driver and connecting that one, so you can remove Rivolt from the Rivian app at any time. Your Rivian password is used once to sign in and is never stored; what we keep is your Rivian email and an encrypted session token. Details: [Is it safe to connect?](/safe-to-connect)

**I connected, but I don't see my truck or any data.**
The usual cause is a driver account whose invite has not been accepted yet: sign in to the Rivian app on a phone with that account once, then tap Check again in Rivolt. Drives and charges appear as the truck is used.

**What is free?**
Recording every drive and charge on every connected vehicle; Now, Activity, Money and Truck for the last 90 days; the monthly statement and its email, with a CSV download for each of the three months the statement shows; a single-day trip planner with charging stops; three saved trips; share cards; imports from ElectraFi, TezLab and ABRP; every alert (charge done, plug-in reminder, left open, software updates, anomalies); and the full export of everything Rivolt holds, including history older than 90 days. One vehicle is shown at a time. New accounts get 14 days of Pro to try. On the web no card is taken and the trial ends on its own; in the iPhone app it is Apple's free trial, which renews as a paid subscription unless you cancel at least 24 hours before it ends.

**What does Pro cost?**
Rivolt Pro is $3.99 a month or $34.99 a year, the same price on the web and in the iPhone app. It adds unlimited history and trends, range near a temperature, multi-day plans with hotel and campground overnights, re-plan from where the truck is, plan-vs-actual after a trip, unlimited saved trips, every vehicle shown, and the Lock Screen widgets and charging Live Activity on iPhone. Accounts that existed when Pro launched have it free for a year; we emailed each of them the date it ends. Prices are in US dollars; web payments are through Stripe, iPhone purchases through Apple, and Rivolt never sees your card.

**What happens if I stop paying?**
Nothing is deleted. Your account goes back to the free tier: the last 90 days stay visible, everything older stays stored and stays in the free export, and it all reappears if you subscribe again. Cancel on the web from Settings → Plan (it opens Stripe's portal, no questions asked) or on iPhone from your Apple subscriptions. Web subscriptions can be refunded within 14 days by emailing us; Apple handles refunds for purchases made in the app. If Rivolt cannot reach Rivian for 30 consecutive days, or we stop the service, we refund the unused part of your Pro period.

**Does Rivolt wake my truck?**
Rivolt sends no commands to your truck (no locks, climate or charging controls). Reading its data can occasionally wake it.

**How do I disconnect Rivolt from my Rivian?**
In Settings → Account, choose Sign out of Rivian; Rivolt stops reading your truck. If you connected an Authorized Driver account, you can also remove that driver in the Rivian app, which cuts access from Rivian's side.

**Where is my data, and how do I export or delete it?**
Your data is stored on servers we run in the United States. On rivolt.dev, open Settings → Data to download everything as a zip of CSV files or a full JSON backup, or to delete your account. Deleting removes your data; copies can remain in logs for up to 90 days and in backups for up to a year, as the Privacy Policy describes. The iPhone app links to the same page.

## Legal

[Privacy Policy](/privacy) · [Terms](/terms) · [Source code (MIT)](https://github.com/apohor/rivolt)

Rivolt is unofficial, not affiliated with Rivian.
