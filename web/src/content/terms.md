# Rivolt Terms of Service

Last updated: LAUNCH_DATE

## 1. What Rivolt is, and who runs it
Rivolt is an independent, open-source companion app that records and analyses data from your Rivian vehicle. It is not affiliated with, endorsed by, or partnered with Rivian Automotive, Inc. "Rivian" is a trademark of its owner, used descriptively.

These Terms are an agreement between you and ZeroDowntime LLC, a Texas limited liability company, which runs Rivolt ("we", "us"). They apply to the hosted service at rivolt.dev. Self-hosted installs are covered by the MIT license alone (section 9). By creating an account, you agree to these Terms and confirm that you have read the [Privacy Policy](/privacy). How the Terms apply to accounts created before they were published is in section 11.

## 2. Unofficial access to Rivian: the main risk
Rivolt reads your vehicle data through Rivian's unofficial, undocumented interfaces, the same ones Rivian's own app uses. Rivian does not offer these interfaces to third parties, has not approved Rivolt, and may change, limit or block access at any time. You accept that:
- Rivolt may stop working, show stale or wrong data, or be unavailable for long periods without notice.
- Rivian's own [terms](https://rivian.com/legal/terms-of-use) say you should not use your Rivian username or password with applications, websites or software Rivian has not approved, and Rivian has not approved Rivolt. Connecting Rivolt may therefore break your agreement with Rivian, whichever Rivian account you use.
- Rivian could, in principle, act against accounts that use unofficial access (for example by limiting or suspending the account). We have no control over that. Using a dedicated Authorized Driver account (section 4) limits the exposure of your main account.
- Rivolt's calls to Rivian can count against limits on your account. If you see problems in the official Rivian app (for example "No Cloud Connection"), disconnect Rivolt and contact us.
- Rivolt currently presents itself to Rivian as Rivian's own mobile app: REST requests use the iOS app's User-Agent and the Android app's client-name and client-version headers (`com.rivian.android.consumer`), and the first WebSocket message (`connection_init`) carries the same Android client name and a client id of the form `m-ios-<uuid>` (`dc-cid`). Rivolt also sends its own `X-Rivolt-Version` header on REST requests and on the WebSocket connection request, so Rivian can tell Rivolt's traffic apart. This is part of what makes the access unofficial and could be a reason for Rivian to act against it.
- Rivolt is read-only: it does not send commands to your vehicle (the only mutations it issues are sign-in calls).

**If access to Rivian ends for good.** If Rivian blocks Rivolt permanently, or we decide to stop the hosted service, we will email you at least 30 days before we shut it down, and your stored history stays available to view and export until then. There is nothing to refund on the free tier. If you pay for Rivolt Pro and we shut the hosted service down, or Rivolt cannot read data from Rivian for 30 consecutive days (counted from the first day on which no account on the service receives data from Rivian, and ending on the first day data from Rivian flows again; the demo truck and file imports do not count), we refund the unused part of your current Pro period, in proportion to the days left (section 5).

## 3. Who can use Rivolt, your account and Rivian sign-in
- You must be **18 or older** and live in the United States (Privacy Policy, "Who it is for").
- You sign in to Rivolt with Google, with GitHub, or with your email and a password through Rivolt's own sign-in.
- To connect a vehicle you enter your Rivian email and password, and a one-time code if Rivian asks for one. Rivolt uses these only to sign in to Rivian on your behalf. See the Privacy Policy for what is stored.
- You must be the owner or an authorised driver of the vehicle, and entitled to share its data. You are responsible for activity on your account.

## 4. Authorized Driver account (recommended)
We recommend connecting a second, free Rivian account that you add as an Authorized Driver on your vehicle, instead of your primary Rivian login. You can still connect your main account; the Authorized Driver account is the safer default. Benefits: Rivolt uses that account's credentials, not yours; you can remove Rivolt with one click in the Rivian app, without changing your main password; and Rivolt's requests count against that account's limits rather than your main account's, so they are less likely to affect the official Rivian app on your phone. Trade-off: an Authorized Driver account may not see all of the history your primary account holds (for example, charges started by the owner). Step-by-step setup: [docs/SIGNUP.md](https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md).

## 5. Free tier, Rivolt Pro and the beta
Rivolt was free during its public beta. From LAUNCH_DATE the hosted service has a **free tier** and a paid plan, **Rivolt Pro**. The current limits of each are listed on the [support page](/support); in short, the free tier records everything, shows the last 90 days, plans single-day trips and keeps every alert, the full data export and deletion free, always. Pro adds unlimited history, multi-day planning and the other features listed there. We may change what each tier includes; a change that takes something away from the tier you are on is a material change under section 11. The total number of accounts is capped: signing up with Google, GitHub or a verified email works straight away while there is room, and when the cap is reached new signups wait for our approval. The service remains in beta: features may change or be removed.

**Accounts created before LAUNCH_DATE** have Pro at no charge until FOUNDING_END_DATE, with no card and nothing charged. We will email you and show a notice in the app at least 30 days before that date. After it, the account is on the free tier unless you subscribe; nothing is deleted.

**Paying for Pro.** Pro costs US$3.99 a month or US$34.99 a year. On the web you pay through Stripe; in the iPhone app through Apple's In-App Purchase. Rivolt never sees or stores your card. Purchases on rivolt.dev are available to customers with a US billing address; if a web purchase goes through without one, we refund it in full and end the subscription (or email us). You accept the billing terms below when you subscribe; they apply only to subscribers.
- **Renewal.** A subscription renews automatically at the end of each period, at the price shown when you subscribed, until you cancel. For yearly plans bought on the web we email a reminder at least 30 days before each renewal.
- **Cancelling.** On the web, Settings → Account → Plan opens Stripe's customer portal and cancels in one step, no questions asked; in the app, cancel in your Apple subscriptions. Cancelling stops the next charge; Pro stays on until the period you paid for ends.
- **Price changes.** We will email you at least 30 days before a price change applies to you, and you can cancel before it does.
- **Refunds.** Web purchases: a full refund within 14 days of the first charge, no questions asked, by emailing support@rivolt.dev. Apple purchases: Apple decides refunds under its own terms; ask Apple. Either way, the pro-rata refund in section 2 applies if the service stops or Rivian access is lost for 30 consecutive days.
- **If Pro ends** (you cancel, a payment fails after a 7-day grace period, or the free year ends), your account goes back to the free tier. Nothing is deleted: history outside the free window stays stored, stays in the free export, and reappears if you subscribe again.
- **Deleting your account** cancels any web subscription; cancel an Apple subscription yourself first, because Apple does not let us.

## 6. Acceptable use
Don't: connect a vehicle you aren't authorised to access; use Rivolt to track or monitor a person without their knowledge and consent; attempt to access other users' data; probe, overload or circumvent limits of Rivolt or Rivian; resell the service; or use it where failure could endanger anyone. Rivolt records the vehicle, not the driver: if other people drive your vehicle, their trips and locations are recorded in your account, and you are responsible for telling them. Rivolt is informational and must not be relied on for safety, charging, range or legal/tax decisions without checking the source. **Do not rely on Rivolt for safety:** tire pressure, charge level, range, alerts and notifications can be late, missing or wrong, so check your vehicle and the Rivian app.

## 7. Your data
You keep ownership of your data. You can export it and delete it at any time (Privacy Policy, "Keeping, exporting and deleting"). We use it only to provide the service and as described in the Privacy Policy.

## 8. No warranty, limited liability
- The service is provided "as is" and "as available", without warranties of any kind, express or implied, including merchantability, fitness for a particular purpose, accuracy of costs, distances, energy or battery figures, or availability.
- To the maximum extent permitted by law, we are not liable for indirect, incidental, special or consequential loss, lost data or lost profits, or for loss arising from Rivian's actions, including changes to or blocking of its interfaces, or action Rivian takes against your Rivian account.
- Our total liability for all claims relating to Rivolt is limited to the greater of the amount you paid us in the 12 months before the claim and US$100.
- Nothing in these Terms limits liability that the law does not allow to be limited (for example for fraud, gross negligence or willful misconduct), or takes away consumer rights that cannot be waived under the law of the state where you live.

You agree to cover our reasonable losses and costs from a claim by someone else that arises from your breach of section 3 or 6 (for example, connecting a vehicle you were not entitled to connect, or tracking a person without consent). This does not cover claims that come only from using Rivolt as intended, including connecting your Rivian account to it (section 2).

## 9. Open source and self-hosting
Rivolt's source code is MIT licensed. If you self-host, these Terms and our Privacy Policy do not apply: you run the software, hold the data and carry the risks in sections 2 and 8 yourself, and the MIT license's no-warranty terms apply. We offer no support or availability commitment for self-hosted installs. Self-hosting is not a way around Rivian's rules; the same unofficial-access risk applies.

## 10. Suspension and termination
You can stop at any time by deleting your account. We may suspend or end the service or an account (for example to comply with a Rivian request, protect other users, or for abuse). Where practical we will give notice and let you export your data first. Accounts that never connect Rivian are deleted as described in the Privacy Policy ("Keeping, exporting and deleting": warning at day 90, deletion at day 120 and never less than 30 days after the warning). We may pause all Rivian access at any time using an operator kill switch.

## 11. Changes, contact, disputes and law
- **Accepting these Terms.** New users agree to these Terms when they sign up. If your account was created before LAUNCH_DATE, these Terms apply to your use of Rivolt from TERMS_EFFECTIVE_EXISTING (30 days after we emailed you about them), and the [previous Terms of October 23, 2026](/terms/2026-10-23) until then; if you do not agree with them, stop using Rivolt and delete your account (Privacy Policy, "Keeping, exporting and deleting").
- **Changes.** We may update these Terms. We will tell you about material changes by email and with an in-app notice at least 30 days before they take effect (the same as the Privacy Policy, "Changes"). If you keep using Rivolt after a change takes effect, the new Terms apply; if you do not agree, delete your account before then.
- **Transfer.** We may transfer these Terms and the service to an affiliated company or to a new owner. We will email you at least 30 days beforehand, so you can export or delete your data first (Privacy Policy, "Changes").
- **Contact.** support@rivolt.dev.
- **Disputes.** Before filing a claim, email us and give us 30 days to try to resolve it. Either of us may bring an individual claim in small-claims court. There is no arbitration clause.
- **Governing law and venue.** The laws of the State of Texas, without regard to its conflict-of-laws rules, govern these Terms. Any claim not brought in small-claims court must be brought in the state or federal courts for Williamson County, Texas, unless the law of the state where you live gives you the right to sue there.
- **Whole agreement.** These Terms and the Privacy Policy are the whole agreement between you and us about the hosted service. If a court finds part of these Terms unenforceable, the rest still applies.
