# Rivolt Terms of Service (hosted service)

> **DRAFT. Not legal advice. Needs review by a qualified lawyer before publication.**
> Items marked **[DECIDE]** are owner decisions. Items marked **[VERIFY]** must be re-checked against the code at publication time.
> Applies only to the hosted service at rivolt.dev. Self-hosted installs are governed by the MIT license alone (section 9).

Last updated: DRAFT 2026-10-13

## 1. What Rivolt is
Rivolt is an independent, open-source companion app that records and analyses data from your Rivian vehicle. It is not affiliated with, endorsed by, or partnered with Rivian Automotive, Inc. "Rivian" is a trademark of its owner, used descriptively.

## 2. Unofficial access to Rivian: the main risk
Rivolt reads your vehicle data through Rivian's unofficial, undocumented interfaces, the same ones Rivian's own app uses. Rivian has not authorised this and may change, limit or block it at any time. You accept that:
- Rivolt may stop working, show stale or wrong data, or be unavailable for long periods without notice.
- Rivian could, in principle, act against accounts that use unofficial access (for example by limiting or suspending the account). We have no control over that. Using a dedicated Authorized Driver account (section 4) limits the exposure of your main account.
- Rivolt's calls to Rivian can count against limits on your account. If you see problems in the official Rivian app (for example "No Cloud Connection"), disconnect Rivolt and contact us.
- Rivolt currently presents itself to Rivian as Rivian's iOS app: REST requests use the iOS app's User-Agent and client-name headers, and the first WebSocket message (`connection_init`) labels Rivolt as an iOS device with a client id of the form `m-ios-<uuid>` (`dc-cid`). Rivolt also sends its own `X-Rivolt-Version` header on REST requests, and on the WebSocket once change #137 ships. This is part of what makes the access unofficial and could be a reason for Rivian to act against it.
- Rivolt is read-only: it does not send commands to your vehicle (the only mutations it issues are sign-in calls).

## 3. Your account and Rivian sign-in
- You sign in to Rivolt through a supported identity provider (OIDC). **[VERIFY: list providers enabled in prod]**
- To connect a vehicle you enter your Rivian email and password, and a one-time code if Rivian asks for one. Rivolt uses these only to sign in to Rivian on your behalf. See the Privacy Policy for what is stored.
- You must be the owner or an authorised driver of the vehicle, and entitled to share its data. You are responsible for activity on your account.

## 4. Authorized Driver account (recommended)
We recommend creating a separate free Rivian account, adding it as an Authorized Driver on your vehicle, and connecting Rivolt with that account. Benefits: your primary credentials never touch Rivolt, and you can revoke Rivolt from the Rivian app. Step-by-step: docs/SIGNUP.md. Note: Authorized Drivers may see only part of the history Rivian holds. **[VERIFY wording against SIGNUP.md]**

## 5. Free service, beta, and pricing
The hosted service is currently free and invite-only beta. **[DECIDE: if a paid plan launches, add: price, billing period, refund policy (suggest 14 days), what happens to data on cancellation (suggest read-only access and export for 6 months), and a 30-day notice period for price changes.]** Features may change or be removed.

## 6. Acceptable use
Don't: connect a vehicle you aren't authorised to access; attempt to access other users' data; probe, overload or circumvent limits of Rivolt or Rivian; resell the service; or use it where failure could endanger anyone. Rivolt is informational and must not be relied on for safety, charging, range or legal/tax decisions without checking the source.

## 7. Your data
You keep ownership of your data. You can export it and delete it at any time (Privacy Policy, section 5). We use it only to provide the service and as described in the Privacy Policy. Aggregated, anonymised statistics may be used to improve the service. **[DECIDE: confirm, or remove this sentence.]**

## 8. No warranty, limited liability
The service is provided "as is" and "as available", without warranties of any kind, including accuracy of costs, distances, energy or battery figures, or availability. To the maximum extent permitted by law we are not liable for indirect or consequential loss, or for loss arising from Rivian's actions, including changes to or blocking of its interfaces. Our total liability is limited to the amount you paid in the 12 months before the claim (zero while free). **[LAWYER: jurisdiction, consumer-law carve-outs, governing law.]**

## 9. Open source and self-hosting
Rivolt's source code is MIT licensed. If you self-host, these Terms and our Privacy Policy do not apply: you run the software, hold the data and carry the risks in sections 2 and 8 yourself, and the MIT license's no-warranty terms apply. We offer no support or availability commitment for self-hosted installs. Self-hosting is not a way around Rivian's rules; the same unofficial-access risk applies.

## 10. Suspension and termination
You can stop at any time by deleting your account. We may suspend or end the service or an account (for example to comply with a Rivian request, protect other users, or for abuse). Where practical we will give notice and let you export your data first. We may pause all Rivian access at any time using an operator kill switch.

## 11. Changes, contact, law
We may update these Terms; we will notify signed-in users of material changes. Contact: **[DECIDE: address]**. Governing law and venue: **[LAWYER]**.
