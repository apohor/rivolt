# DCFC pricing - rate table reference

The trip planner's cost strip ("DCFC spend: $X guest / $Y with
memberships") is computed from a small per-network rate table in
[`internal/tripadvice/networks.go`](../internal/tripadvice/networks.go).
Rivian's `planTripWithMultiStopV2` returns each stop's charger name
but no pricing - we substring-match the name to a network row in
this table to get a rate.

This page documents the rates, where they come from, and how to
update them when the operator changes their pricing page.

## Rates (USD/kWh, US deployments, mid-2026)

| Network | Guest | Member | Plan | Source |
|---|---|---|---|---|
| Electrify America | 0.48 | 0.36 | Pass+, $7/mo | [electrifyamerica.com/pricing](https://www.electrifyamerica.com/pricing/) |
| Tesla Supercharger (non-Tesla) | 0.55 | 0.40 | Supercharging Membership, $12.99/mo | [tesla.com/support/charging](https://www.tesla.com/support/charging) |
| Rivian Adventure Network | 0.45 | n/a | Rivian owner only (no separate tier) | [rivian.com/support/article/rivian-adventure-network](https://rivian.com/support/article/rivian-adventure-network) |
| EVgo | 0.42 | 0.34 | Rewards+, $6.99/mo | [evgo.com/charging-plans](https://www.evgo.com/charging-plans/) |
| Blink | 0.49 | 0.39 | Member, annual fee | [blinkcharging.com](https://www.blinkcharging.com/) |
| bp pulse | 0.45 | 0.39 | Plus, $4/mo | [bppulse.com](https://www.bppulse.com/) |
| Shell Recharge | 0.43 | 0.40 | GO+, free | [shellrecharge.com](https://shellrecharge.com/) |
| ChargePoint | 0.45 | n/a | Host-set, no central plan | [chargepoint.com](https://www.chargepoint.com/) |
| Francis Energy | 0.40 | n/a | No public plan | [francisenergy.com](https://francisenergy.com/) |
| Ionna | 0.40 | n/a | Limited deployment; pricing TBD | [ionna.com](https://www.ionna.com/) |
| Flo | 0.35 | n/a | No US plan | [flo.com](https://www.flo.com/) |
| **Unmatched DCFC** | **0.46** | n/a | Fallthrough when name doesn't match | n/a |

> **Rivian Adventure Network** is Rivian-only by access. Every
> Rivolt user is by definition a Rivian owner, so the "member"
> rate is auto-applied. The table shows the same number in both
> columns.

> **Links verified manually.** Tesla's deep-link to a non-Tesla
> pricing page churns regularly, so this table points at the
> stable top-level support page; navigate from there to "Using
> Superchargers with non-Tesla vehicles".

## How matching works

Per planned stop, [`MatchNetwork(name)`](../internal/tripadvice/networks.go)
lowercases the charger name and substring-checks against each
network's `MatchPatterns` in table order. First hit wins.
**Order matters**: `tesla supercharger` is listed before any
generic `tesla` pattern so a future Tesla Destination L2 site
doesn't accidentally claim the Supercharger rate.

The unmatched fallback at `$0.46/kWh` is the conservative middle
of the table, chosen so a single unmatched stop on an otherwise
known route doesn't dominate the total.

## Caveats

These rates are **best-effort national averages** with the
following known gaps:

- **Regional variance is real.** EA in CA is closer to $0.56, EA
  in TX is $0.48. The flat number is wrong by ±10% in either
  direction at any specific site. Adding state-level multipliers
  is on the roadmap; not v1.
- **Time-of-day variance is real.** Tesla Supercharger pricing
  swings $0.20/kWh between off-peak and peak. We pick the
  mid-band.
- **Idle / congestion fees ignored.** EA charges $0.40/min after
  10 min idle, Tesla non-Tesla congestion fee $0.13/min. Both
  add to the actual bill but aren't in this estimate.
- **Member rate assumes the user pays the plan fee.** We don't
  amortize the monthly cost. If the user only takes one trip a
  month, EA Pass+ ($7/mo / 50 kWh saved = $0.14/kWh effective
  saving, not $0.12). The cost strip is the per-trip view, not
  the annual-budget view.
- **Operator self-reporting via NREL is patchy.** The chargers
  archive ingests `ev_pricing` from NREL AFDC but we don't yet
  use it per-stop. That's slice B (deferred). When wired, the
  per-stop quote can override the network default with the
  operator-reported number.

## Update cadence

No formal cadence today. Triggers for updating a row:

- An operator publishes a rate change (EA's March 2023 hike
  from $0.43 -> $0.48 was the canonical example).
- A new network reaches Rivolt's user base (Ionna, e.g.).
- Multiple Rivolt users report a meaningful gap between the
  table rate and their session receipts. **The long-term fix
  is slice C**, replacing the table with each user's rolling
  average from their own `charges` history. Until then the
  table is the bridge.

The test in `internal/tripadvice/networks_test.go` pins the
match-substring -> slug pairs against real Rivian planner names
sampled in production, so a name format change ("Electrify
America - Pflugerville" to some new format) gets flagged on the
next CI run.

## Adding a network

1. Add a `Network` row to `Networks` in
   [`internal/tripadvice/networks.go`](../internal/tripadvice/networks.go).
   Order by specificity (more specific patterns first).
2. Add at least one substring pattern that matches the planner's
   charger name. Lowercase, with leading/trailing spaces if you
   need word-boundary behavior (` flo ` not `flo`).
3. Drop a test case into `TestMatchNetwork_RealRivianNames` with
   a name you've actually seen from Rivian's planner.
4. Update this docs table with the source URL.
5. Run `go test ./internal/tripadvice/...` to make sure the
   sanity test (`TestRateTableSanity`) still passes. It pins
   guest > member and rates in (0, $2/kWh).
