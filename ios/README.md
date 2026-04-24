# Rivolt iOS

Native iOS client for a self-hosted Rivolt server. v0.9 scope: sign in,
see state-of-charge + estimated range for your first vehicle. Everything
else (live panel, push, Live Activities, CarPlay, widgets) is deferred.

Current install target: **Xcode Run on one tethered iPhone**. No
TestFlight, no Ad-hoc, no distribution pipeline yet. See
`docs/ROADMAP.md` in the repo root for the wider v0.9 plan.

## Prerequisites

- macOS 14+ (Sonoma) with **Xcode 15.3+** installed from the Mac App Store.
- A **paid Apple Developer Program** membership ($99/yr) signed in
  under *Xcode → Settings → Accounts*. A free Personal Team works for
  compile+run, but the v0.9 roadmap needs push / Live Activities /
  App Groups entitlements that only the paid program grants. Set it
  up once at the start so you're not refactoring entitlements mid-way.
- **xcodegen** — `brew install xcodegen`. The `.xcodeproj` is
  generated from [`project.yml`](project.yml) and not checked in;
  this keeps merge conflicts out of the Xcode project file.
- A running Rivolt backend reachable from the phone. The default URL
  (`https://rivolt.apoh.synology.me`) is the operator's prod host;
  override it on the login screen for local dev.

## First run

```sh
cd ios
make open         # regenerates Rivolt.xcodeproj and opens Xcode
```

In Xcode:

1. Select the **Rivolt** scheme and your connected iPhone as the
   run destination (top bar).
2. *Signing & Capabilities* → set **Team** to your paid developer
   team. Xcode will auto-provision a development profile; the
   bundle ID (`me.apoh.rivolt`) can stay as-is for now.
3. Hit ⌘R.
4. On the phone, accept the developer trust prompt:
   *Settings → General → VPN & Device Management → trust the
   profile*. Only needed once per device per developer account.

The app opens to a login screen. Server URL is prefilled; enter your
Rivolt username + password (the same `RIVOLT_USERNAME` / `RIVOLT_PASSWORD`
the web UI uses). Session cookie persists across app restarts, so
cold launch lands you on the home screen directly.

## Workflow

After editing source files, you usually don't need to regenerate the
project — Xcode picks up changes under `Rivolt/` automatically
*because* the target's `sources:` entry is a folder reference. The
exceptions:

- Adding a **new folder**, changing **build settings**, or bumping
  the deployment target → `make project` to regenerate.
- Changing **bundle ID** → `make project` + re-sign in Xcode.

## Troubleshooting

- **"Could not launch Rivolt"** right after trusting the profile:
  unlock the phone first, then tap the app icon once. Xcode sometimes
  races the trust handshake.
- **401 on login** but credentials are correct: ensure the server
  URL has the right scheme (`https://`, no trailing slash). The Rivolt
  backend only sets the session cookie on secure origins unless you
  explicitly opted out server-side.
- **Network error** against localhost: iOS 14+ blocks cleartext HTTP.
  Run the backend over HTTPS (e.g. behind Caddy / traefik on your
  LAN) or add an ATS exception in `project.yml` for dev builds.
