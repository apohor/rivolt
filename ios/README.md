# Rivolt iOS

Native iOS client for Rivolt. It signs in to **https://rivolt.dev** by default;
"Other server" on the sign-in screen points it at a self-hosted Rivolt (https
only, checked against the server's `/api/app/config`).

Sign-in goes through the server's own web sign-in in an
ASWebAuthenticationSession (PKCE, a one-time code, then a bearer token in the
Keychain, per server). rivolt.dev comes back on its Universal Link
(https://rivolt.dev/app/callback); any other server on `rivolt://auth`, so a
self-hosted server sets `RIVOLT_APP_CALLBACK_URL=rivolt://auth`. The server
offers app sign-in only when that variable is set (`/api/app/config`
`app_login`). Design: the team's `ios-app-design.md` §2.

## Building

Nobody builds this locally (no heavy local builds on the team's Mac). CI does:
`.github/workflows/ios.yml` generates the project with XcodeGen, then builds and
runs the unit tests on an iOS Simulator with the pinned Xcode, without signing.
Look at the job's summary for the test counts and at the uploaded
`ios-xcresult` artifact when it fails.

If you do have a Mac with Xcode:

```sh
cd ios
make open         # regenerates Rivolt.xcodeproj from project.yml and opens it
```

The `.xcodeproj` is generated from [`project.yml`](project.yml) and not
checked in. Regenerate (`make project`) after adding a folder or changing build
settings.

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

## Installing on a phone

Through TestFlight, once the Apple Developer account exists (owner item N28).
Until then there is no install path.
