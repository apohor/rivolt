# Rivolt iOS

The native iPhone app for Rivolt: the **Now** card, charge and range
**widgets** (Home Screen and Lock Screen) and **notifications**. It is
read-only: it never sends anything to the truck.

## Installing

Through **TestFlight**, as an invited internal tester: invite-only, with no
public link and no App Store listing yet. You get an invitation by email,
install TestFlight from the App Store, and accept it. Builds are uploaded from
tagged releases (`ios-v*`).

## Signing in

The app opens a browser sheet on **rivolt.dev** and signs in the way the web
app does (Rivolt's own sign-in; the app never asks for or sees a password),
then keeps a session token in the iPhone Keychain. Sign out removes it. The
token is tied to the server that issued it and is only ever sent there.

**Self-hosters:** "Other server" on the sign-in screen accepts an https address
and checks it against the server's `/api/app/config`. The server enables app
sign-in with `RIVOLT_APP_CALLBACK_URL=rivolt://auth`. Notifications in the App
Store build come only from rivolt.dev (they need Rivolt's Apple key); a
self-hosted server can sign in and read data, but not push to that build.

## Building (contributors)

Nobody builds this locally (no heavy local builds on the team's Mac). CI does:
`.github/workflows/ios.yml` generates the project with XcodeGen, then builds and
runs the unit tests on an iOS Simulator with the pinned Xcode, without signing.
Look at the job's summary for the test counts and at the uploaded
`ios-xcresult` artifact when it fails.

The `.xcodeproj` is generated from [`project.yml`](project.yml) and not
checked in (`make project` regenerates it; `make open` also opens it, for
someone who does have Xcode). Look at the uploaded `ios-snapshots` and
`ios-screenshots` artifacts for how the screens render in light and dark.

## Widgets

Charge and range on the Home Screen (small, medium) and the Lock Screen
(circular, rectangular, inline). They are read-only and do nothing when tapped
except open the app. A widget reads the same two endpoints the Now card does
(`/api/state/{id}` and `/api/vehicles/{id}/now`) on your Rivolt server only,
with the token from the shared Keychain group, and never talks to Rivian. Old
data says so ("Updated 3 hr. ago", dimmed, "(old)" on the Lock Screen) instead
of looking current. The App Group `group.dev.rivolt.app` holds only which truck
and server to read and the last numbers shown (no VIN, name or location), and is
cleared on sign-out, on switching server and when the server refuses the token.
The system decides how often a widget refreshes; Rivolt asks for about every
15 minutes while charging or driving, 30 parked and 60 asleep or stale.

## Notifications (push)

After signing in, and only when the server's `/api/app/config` says
`push.apns` is true, Home shows Rivolt's own "Get a notification when charging
finishes" card; "Turn on" raises the iOS prompt, "Not now" is remembered.
Settings → Notifications (Home menu) has the same five switches as the web
(charging complete, software updates, anomalies, plug-in reminder and
milestones, the last two off until turned on).

An APNs payload carries only `{id}` and a generic alert: Apple's push
servers see that a notification was sent, never its words. The Notification
Service Extension (`RivoltNotificationService`) fetches the title and body from
`GET /api/push/notifications/{id}` with the signed-in token, shared through the
Keychain access group, and falls back to the generic alert if that fails. The
entitlements (push, the shared Keychain group) apply to Release builds only, so
the unsigned simulator build in CI is unchanged. Real pushes need the Apple
Developer account (N28).

CI also launches the app with `-rivolt-preview <prompt|notifications|notifications-denied>`
(fixed screens, no network) and uploads simulator screenshots in light and dark
as the `ios-screenshots` artifact.
