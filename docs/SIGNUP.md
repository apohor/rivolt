# Signing up for Rivolt

This walks you from signing up for the beta through your first
planned trip. It assumes you're using the hosted instance at
[rivolt.dev](https://rivolt.dev); local-development setups skip
the OIDC steps and use the docker-compose login instead.

---

## 1. Sign up

Rivolt is in public beta, no invite needed; total accounts are capped. There are two
ways in:

### Option A — Continue with Google or GitHub (fastest)

1. Open <https://rivolt.dev/signup> and click **Continue with
   Google** or **Continue with GitHub**.
2. Approve the OAuth consent on the provider's screen
   (scopes: email + basic profile).
3. You're dropped straight into Rivolt with a session cookie.
   No password to set, no approval email to wait for.

If the beta is at capacity, the OAuth callback bounces you to a
"we're at capacity" page with your email pre-filled — leave the
request and we'll let you know when a seat opens up.

### Option B — Request access by email (manual approval)

If you'd rather not link a third-party account, leave your email
on the signup page and we'll send a one-click signup link once
approved:

1. Open <https://rivolt.dev/signup>, enter your email, click
   **Request access**.
2. Wait for the approval email from `anton@rivolt.dev` (usually
   the same day — check spam if it doesn't appear; iCloud Mail
   in particular tends to filter new senders). The link is
   single-use and expires in 14 days.
3. Click **Finish signup** in the email. The signup form opens
   with your email already filled in — provide a display name
   and a 12+ character password.
4. Click **Create account**. You're signed in straight away.

The token is consumed on success and your identity is provisioned
at `auth.rivolt.dev` (Ory Kratos under the hood).

You land on the **Connect your Rivian** screen — the next step
connects your truck.

---

## 2. Connect your Rivian account

Rivolt uses your Rivian credentials to subscribe to the live
telemetry WebSocket and read your drive / charge history. Calls
are **read-only**; Rivolt has never sent a command to your
vehicle. The only write-style requests it makes to Rivian are the
sign-in steps.

Tip: connect a dedicated Authorized Driver account (see
"Recommended" below) if you can; the steps here are the same.

1. Right after signup Rivolt shows the Rivian sign-in form. (Later,
   it's on **Now** until you're connected, and always under
   **Settings → Account → Rivian account**.)
2. Enter the email + password you use in the Rivian app.
3. Click **Sign in**.

If Rivian prompts for a one-time code, copy it from your phone /
email and submit it. Rivolt seals the resulting session token
locally; the password itself is never stored.

After ~30 seconds the live panel on **Now** should start
showing your vehicle's current SoC, location, and gear. The first
WebSocket connection also pulls the last few drives + charges into
your history.

Once signed in, Rivolt lists the vehicles it found on that Rivian
account. If it says **No vehicles on this Rivian account**, the
sign-in worked but there's nothing to record — usually an Authorized
Driver account whose invite hasn't been accepted yet (see step 4
below). Fix it in the Rivian app, then click **Check again**.

### Recommended: connect a dedicated Authorized Driver account

We recommend connecting a second, free Rivian account added as an
Authorized Driver on your vehicle, instead of your primary login.
You can still connect your main account; this is the safer default.
Benefits:

- Rivolt uses *its* credentials, not yours.
- Removing Rivolt is a one-click revocation in the Rivian app —
  no need to rotate your main account password.
- A compromised Rivolt instance can't read your primary Rivian
  account metadata / inbox.
- Rivolt's requests count against the dedicated account's limits,
  not your main account's, so they are less likely to affect the
  official Rivian app on your phone.

Trade-offs, honestly:

- You need a second email address and a few minutes in the Rivian
  app (the steps below).
- An Authorized Driver account may not see all of the history that
  your primary account holds (for example charges started by the
  owner). Live data from the moment you connect is complete.
  <!-- VERIFY: exact visibility limits for Authorized Drivers -->
- Rivolt is unofficial and Rivian can change or block access at any
  time, with either kind of account.

To set it up:

1. Create a new Rivian account at <https://rivian.com> with a
   different email from your primary one. A `+rivolt` Gmail alias
   works (`yourname+rivolt@gmail.com`).
2. In the Rivian app on your phone (logged in as your **primary**
   account), open **Vehicle → Settings → Authorized Drivers** and
   add the new email.
3. Open the invite email in the new account's inbox and click
   **Accept**.
4. **Important:** sign in to the Rivian app on a phone with the
   *new* (dedicated) account. Until the dedicated account
   authenticates in the app at least once, the invite stays in
   "Sent" status and the vehicle isn't actually added — Rivolt
   won't see it. Once you sign in, the invite flips to active and
   the vehicle appears in the dedicated account's vehicle list.
5. Verify the new account can see your vehicle in the Rivian app,
   then connect it in Rivolt using the steps above.

---

## 3. (Optional) Import ElectraFi history

If you've been using ElectraFi, you can backfill years of drives
and charges:

1. In the ElectraFi app, go to **Settings → Account → Advanced →
   Download ElectraFi Data**. You'll get a set of CSV files — one
   `drives.csv` and one `charges.csv` per month.
2. In Rivolt, go to **Settings → Data → Import ElectraFi CSV**.
3. Select all of the CSVs at once and upload. The importer streams
   each file, deduplicates against any drives / charges Rivolt has
   already recorded live, and merges timestamps so the import
   doesn't collide with your live history.

Rivolt's importer is timezone-aware — make sure the **Display →
Timezone** preference matches the tz the CSVs were exported under
or session IDs won't line up.

---

## 4. (Optional) Configure your home charging cost

Rivolt prices every charging session against the rate you actually
pay. Set it once and every future session reflects it; sessions
recorded before this is set will show $0 until you re-price them.

1. **Settings → Charging → Home charging cost**.
2. Enter your **$/kWh** rate (e.g. `0.12` for $0.12/kWh).
3. Pick your **Currency** (USD, EUR, CAD, etc.).
4. Save.

Rivolt records the rate as of the moment a session ends, so
historic costs don't get rewritten if you change the rate later.

If you have a public-charging-network membership (EA Pass+, EVgo,
etc.) you can also configure preferred networks under **Settings →
Charging → Charging networks** — those are passed to the Rivian
trip planner as soft hints.

---

## 5. Plan your first trip

You're set up. Try the planner:

1. Open the **Plan** tab.
2. The departure picker defaults to "now". Pick a different
   day/time if you're planning ahead — the trip analysis will use
   the forecast for that hour, not today's weather.
3. **From** — defaults to your live vehicle position. Use the
   "Current vehicle position" preset to lock it in, or type any
   place name and pick a result.
4. **To** — type a destination. The geocoder is Photon-backed and
   resolves resort / POI names ("Crested Butte Mountain Resort")
   that the basic city-level geocoder misses.
5. Optionally add **via stops** between From and To.
6. The settings grid (Starting SoC %, Target arrival SoC %, Drive
   mode, Tesla NACS adapter) is just above. Leave defaults if you
   want the planner to pick.
7. Click **Plan trip**.

The result panel shows:

- A map with your route + charger overlay (toggle DCFC / L2 / All
  near the top of the map).
- A summary strip: total distance, total time, arrival HH:MM with
  SoC, charging stops + total charge time.
- A **Trip analysis** card with:
  - **Cost** — DCFC spend in USD (computed from each stop's SoC
    delta × your pack capacity × the per-kWh DCFC rate) and a
    home-rate equivalent for the total energy used.
  - **Efficiency / Weather / Vehicle** sections — only show up
    when there's something material to say (cold-snap headwind
    forecast, underinflated tires, etc.).

The form remembers what you planned last time, so reopening the
page lands you back on the same trip.

---

## 6. Notifications (optional)

To get a push notification when your truck finishes charging
overnight, opt in once per device:

1. **Settings → Notifications**.
2. Click **Enable on this device**. Approve the browser's
   permission prompt.
3. Click **Send test** to verify the channel end-to-end.
4. Toggle the events you care about:
   - **Charging session completes** — fires when a session ends.
   - **Plug-in reminder** *(planned)*.
   - **Anomaly detected** *(planned)*.

iOS Safari requires the site to be **added to the home screen**
before push works (Share → Add to Home Screen).

---

## What's next

- **Plan a road trip.** Open the **Plan** tab and try a real
  destination — the planner picks charging stops, the Trip
  analysis card breaks down DCFC spend + home-rate equivalent +
  weather/efficiency commentary, and the form remembers your
  last setup for the next trip.
- **Set your home location.** **Settings → Vehicle → Home
  location** → click the map or paste lat/lon. Once set, Rivolt
  auto-tags charging sessions started near it as `Home` (instead
  of waiting for clustering to converge) and the trip planner
  offers "Home" as a one-tap preset.
- **Activity** lists every drive with energy used, cost,
  speed averages, and a route preview. Click into one for the
  full timeline (speed / SoC / elevation / weather charts +
  road-snapped route).
  Its Table and Map views show drives and charges as tables and on
  maps. Charging sessions get the same detail — per-session curve, peak
  kW, thermal split, cost.
- **Settings → Display** controls units (mi/km, °C/°F) and your
  timezone.

If something breaks, your data is yours: **Settings → Data →
Danger zone → Backup** dumps everything as JSON; **Reset** wipes
the data while keeping your account + vehicle credentials so
you can re-import cleanly.

For deeper architecture questions see
[`docs/ARCHITECTURE.md`](ARCHITECTURE.md); for what's coming see
[`docs/ROADMAP.md`](ROADMAP.md).

## Self-hosting: closing sign-ups

A self-hosted install that signs in through its own identity provider
(for example Pocket ID) usually doesn't want anyone else creating an
account. Set

```
RIVOLT_SIGNUP_DISABLED=true
```

(default `false`; hosted behaviour is unchanged).

**Who decides what.** With sign-ups disabled, your identity provider
decides who can *authenticate*, and Rivolt decides who *has an
account*. A successful login at the IdP is not enough: the person must
already have a Rivolt account.

What changes:

- the **Create an account** link is hidden on the login page and
  `/signup` says sign-ups are closed;
- `POST /api/signup`, `POST /api/signup/request`, the magic-link
  lookup and the admin access-request routes are not served
  (`/api/config` reports `features.signup_enabled: false`);
- **a first sign-in by an identity that has no Rivolt account is
  refused**, for OIDC and Kratos alike, with "Sign-ups are closed on
  this server". Without this, a successful login at your identity
  provider would still create an account (up to the `signup_cap`
  flag, which defaults to 100).

Still allowed:

- users that already exist;
- users an admin creates. Use **Admin -> Create user** in the web UI,
  or `POST /api/admin/users` with `{"email": "...", "role": "user"}`;
  create the account first, then the person signs in with the same
  email through your IdP;
- the address in `RIVOLT_BOOTSTRAP_ADMIN_EMAIL`, but only when the
  identity provider reports that email as **verified**; an unverified
  address never matches;
- the very first user of an empty install, **only when
  `RIVOLT_BOOTSTRAP_ADMIN_EMAIL` is not set** (the check and the
  insert are serialised, so two simultaneous first logins can't both
  get in). When the variable is set, only that verified address can
  create the first account, so set it on a fresh install: the first
  account is then yours, not whoever signs in first.

Accounts are matched on the lower-cased, trimmed email, so
`Alice@Example.com` and `alice@example.com` are the same account.

## Identity trust: which emails may key or promote an account

Rivolt keys an account on the email your identity provider verified.
Two switches decide how much an address is trusted; the defaults keep
existing installs working.

| Variable | Default | Meaning |
|---|---|---|
| `RIVOLT_OIDC_TRUST_UNVERIFIED_EMAIL` | `false` | When `true`, an email (or an email-shaped `preferred_username`) the IdP did **not** verify may be an account key, as it was in older versions. Unsafe with an IdP that lets anyone register any address; for IdPs that never send `email_verified`. |
| `RIVOLT_TRUST_FIRST_PARTY_EMAILS` | `true` | Rivolt's own Kratos/Hydra only holds identities created through Rivolt sign-up or by an admin, so its email counts as verified even when Kratos email verification is off. One switch for every first-party path: the Hydra `email_verified` claim, the direct Kratos-session path, admin promotion and the sign-ups-closed bootstrap exception. If your Kratos allows open registration, set it to `false`; then only Kratos's own verification state counts. |

What changes with the defaults:

- An identity whose email is **unverified** can no longer sign in as
  the account keyed by that email (or by its `preferred_username`).
  If such an account exists the sign-in is refused with "Your identity
  provider hasn't verified your email address" (verify it at the
  provider and sign in again: you land on the same account); if none
  exists, the identity gets its own isolated account keyed by issuer
  and subject.
- A `preferred_username` is an account key **only for an IdP that
  sends no email at all**. When an email is present it is never keyed
  on (a username is not unique across people: an email local part, a
  display name, a GitHub login). One containing `@` is treated like an
  unverified email.
- `RIVOLT_BOOTSTRAP_ADMIN_EMAIL` promotes only a **verified** email.
- Identities with no email at all keep their issuer+subject key.

### Upgrade note for installs with two or more OIDC providers

A bare `preferred_username` (no `@`) from an IdP that sends no email
keys an account when **one** provider is configured, as before. With
**two or more**, two providers could hand out the same bare name, so
it is an account key only if you opt that provider in:

```
RIVOLT_OIDC_<NAME>_USERNAME_KEY=true     # e.g. RIVOLT_OIDC_CORP_USERNAME_KEY=true
```

Without the opt-in the bare name is handled like an unverified email:
if an account already exists under that name the sign-in is refused
("verify your email" / ask your administrator) rather than silently
creating a second account, otherwise the identity is keyed by issuer
and subject. If your users sign in through a provider that sends no email and
relies on `preferred_username`, add the opt-in before upgrading. If
your provider sends an email it does not verify **and** your accounts
are keyed by `preferred_username`, either enable email verification at
the provider or set `RIVOLT_OIDC_TRUST_UNVERIFIED_EMAIL=true` to keep
the old keying. Providers that send a verified email (Google, GitHub's
primary, first-party Hydra) are unaffected.
