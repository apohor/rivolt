# App Privacy answers for App Store Connect (iPhone 1.4.3)

What the owner enters under App Store Connect > the app > App Privacy. Nothing here is submitted by CI. Sources: `web/src/content/privacy.md` (Last updated November 18, 2026), `ios/Rivolt/PrivacyInfo.xcprivacy` (the app's manifest), the widget and notification-service manifests (collect nothing), and the pinned MapLibre package's own manifest (collects nothing). Not seen: App Store Connect's current form; its screens may be worded a little differently.

## Step 1: "Do you or your third-party partners collect data from this app?"

**Yes.**

## Step 2: data types (tick these seven, nothing else)

For every row: **Linked to the user: Yes. Used for tracking: No. Purpose: App Functionality only** (do not tick Analytics, Product Personalization, Developer's Advertising, Third-Party Advertising or Other Purposes).

| App Store Connect category | Data type | What it is in Rivolt |
|---|---|---|
| Contact Info | Email Address | the account's email |
| Contact Info | Name | the display name |
| Identifiers | User ID | the account id |
| Identifiers | Device ID | the push address (and the sign-in token's device record) |
| Location | Precise Location | where the truck drove, charged and is parked (from the truck, not the phone) |
| Financial Info | Other Financial Info | the cost of each charge |
| Other Data | Other Data Types | vehicle id, battery, tire and other truck readings |

Everything else is **not collected**: no Health, Browsing History, Search History, Contacts, Photos or Videos, Audio, Usage Data (no analytics), Diagnostics (no crash or performance data leaves the app), Purchases (the app sells nothing), Sensitive Info, User Content, or Coarse Location from the phone.

## Step 3: tracking

"Do you or your third-party partners use data for tracking?" **No.** The app has no advertising or analytics SDK and does not ask for tracking permission; the manifest says tracking false.

## Why each gap in the policy is not an extra row

- 90-day technical logs (IP address, account id, vehicle id): Apple's list has no IP-address row; the account id is User ID and the vehicle id is Other Data, both already ticked. The policy carries the 90 days.
- Product analytics: runs on the website only. The iPhone app has no analytics code.
- Optional AI drive analysis (the owner turns it on): covered by Precise Location and Other Data, purpose App Functionality; no advertising.
- Sign in with Google, GitHub or Apple: covered by Email Address, Name and User ID.
- Push: Device ID, already ticked.
- Purchases: no row while the app has no in-app purchase and no purchase button. The submission that adds one adds the row.

## Step 4: the privacy policy URL

`https://rivolt.dev/privacy`. If its text changes before submission, bump its "Last updated" date in the same change.

## Check before pressing Publish

- The label shows seven data types, all "Linked to You", none "Used to Track You".
- Nothing about sign-in on the website changed the app's manifest: if `PrivacyInfo.xcprivacy` gains a data type, this sheet and the label change together.
