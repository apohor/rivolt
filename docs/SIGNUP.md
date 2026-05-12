# Signing up for Rivolt

This walks you from "I have an invite code" through your first
planned trip. It assumes you're using the hosted instance at
[rivolt.dev](https://rivolt.dev); local-development setups skip
the OIDC steps and use the docker-compose login instead.

---

## 1. Redeem your invite code

Rivolt is invite-only while it's growing. If you don't have a code
yet, [open a request on GitHub](https://github.com/apohor/rivolt/issues/new?template=beta-invite-request.yml)
or email <anton@rivolt.dev> with your truck, home charging setup,
and how often you drive.

1. Open <https://rivolt.dev/signup>.
2. Paste the invite code (20-character alphanumeric string).
3. Enter:
   - **Email** — used to sign in and to receive plug-in reminders /
     anomaly notifications.
   - **Display name** — appears next to your drives and charges if
     the household has multiple users.
   - **Password** — 12+ characters. You can change it later from
     the IdP self-service flow at `auth.rivolt.dev`.
4. Click **Create account**.

A successful signup redeems the code (one-shot — keep the codes
secret) and provisions your identity at `auth.rivolt.dev` (Ory
Kratos under the hood). You're routed back to the sign-in screen.

### Signing in

5. Click **Sign in** and authenticate with the email + password
   you just registered. The first sign-in goes through the OIDC
   consent flow — accept the requested scopes (`openid`, `email`,
   `profile`, `offline_access`) once and Rivolt remembers your
   consent for 24 hours.

You land on the Overview page with no data yet. That's expected —
the next step connects your truck.

---

## 2. Connect your Rivian account

Rivolt uses your Rivian credentials to subscribe to the live
telemetry WebSocket and read your drive / charge history. Calls
are **read-only**; Rivolt has never sent a command to a Rivian
endpoint and never will.

### Recommended: dedicated Authorized Driver account

You *can* sign in with your primary Rivian credentials, but we
recommend creating a second Rivian account (free) and adding it as
an Authorized Driver on your vehicle. Benefits:

- Rivolt uses *its* credentials, not yours.
- Removing Rivolt is a one-click revocation in the Rivian app —
  no need to rotate your main account password.
- A compromised Rivolt instance can't read your primary Rivian
  account metadata / inbox.

If you'd rather just sign in with your primary account, skip
ahead to "Add to Rivolt" below.

To set up the dedicated account:

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
5. Verify the new account can see your vehicle in the Rivian app
   before continuing.

### Add to Rivolt

5. In Rivolt, go to **Settings → Account → Rivian account**.
6. Paste the dedicated account's email + password.
7. Click **Sign in**.

If Rivian prompts for a one-time code, copy it from your phone /
email and submit it. Rivolt seals the resulting session token
locally; the password itself is never stored.

After ~30 seconds the live panel on the Overview page should start
showing your vehicle's current SoC, location, and gear. The first
WebSocket connection also pulls the last few drives + charges into
your history.

---

## 3. (Optional) Import ElectraFi history

If you've been using ElectraFi, you can backfill years of drives
and charges:

1. Open the ElectraFi app on your phone and tap **Settings →
   Export data**. You'll get a CSV file emailed to you.
2. In Rivolt, go to **Settings → Data → Import ElectraFi CSV**.
3. Upload the CSV. The importer streams the file, deduplicates
   against any drives Rivolt has already recorded live, and merges
   timestamps so the import doesn't collide with your live history.

Rivolt's importer is timezone-aware — make sure the **Display →
Timezone** preference matches the tz the CSV was exported under
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

1. Open **Plan** in the nav.
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
- The **Drives** page lists every drive with energy used, cost,
  speed averages, and a route preview. Click into one for the
  full timeline (speed / SoC / elevation / weather charts +
  road-snapped route).
- The **Charges** page does the same for charging sessions —
  per-session curve, peak kW, thermal split, cost.
- **Settings → Display** controls units (mi/km, °C/°F) and your
  timezone.

If something breaks, your data is yours: **Settings → Data →
Danger zone → Backup** dumps everything as JSON; **Reset** wipes
the data while keeping your account + vehicle credentials so
you can re-import cleanly.

For deeper architecture questions see
[`docs/ARCHITECTURE.md`](ARCHITECTURE.md); for what's coming see
[`docs/ROADMAP.md`](ROADMAP.md).
