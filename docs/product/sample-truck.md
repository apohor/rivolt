# Sample-truck onboarding (P3)

## Goal
A visitor sees the real product with realistic data before handing over Rivian credentials. Works for signed-out `/demo` (live at [demo.rivolt.dev](https://demo.rivolt.dev), a standalone read-only deployment, and on rivolt.dev once enabled) and as a "Try with a sample truck" step in onboarding.

## Hard rules
- **Generated corpus only.** Never derive from the owner's truck or any real user. No real coordinates near anyone's home. Use public landmarks or synthetic places in one region.
- No Rivian calls, ever. Sample data is read-only.
- Obvious "Sample data" marker on every screen so it can't be mistaken for live.
- Behind a flag, default off.

## Corpus requirements (generator, owned by architect)
One vehicle, 45 days, seeded and deterministic (fixed seed, reproducible in CI). The synthetic "home" must be pinned to a public, non-residential place (e.g. a stadium or park lot) so it can never coincide with a real residence.
| Need | Why |
|---|---|
| About 20 home L2 sessions with 1–3 top-ups per plug-in | exercises receipt grouping |
| 2 road trips, 600+ mi, 3–5 DCFC stops across two networks (e.g. Rivian Adventure Network, Electrify America) | Money split, plan-vs-actual |
| Mix of cost sources: billed, estimated-home, estimated-network, user-entered, one "pending" | source labels |
| One suspect/duplicate session | data-quality note |
| Mostly parked/asleep, 1–2 failed-sleep episodes (a "normal sleeper" majority plus one flicker night) | Now card calm and alert states |
| Currency USD; home rate $0.16/kWh flat | simple to verify |
| Shifted so the last event is "now" | looks live |

Reuse `internal/rivian/replay.go` plumbing where possible; swap the source from a recorded JSONL to generator output.

## Backend contract (architect)
1. Generator command, e.g. `cmd/sampletruck`, writing the fixture JSONL in the replay format.
2. A demo user and vehicle seeded by a migration or a startup hook, flagged `is_sample`, never linked to a real Rivian session.
3. Public read-only access to ledger and state endpoints for the sample vehicle, rate-limited, no cookies required; all other vehicles remain 401.
4. Visitors can't write: settings, prices, vehicle controls return 403 for the sample vehicle.
5. Optional "per-visitor" time shift is NOT needed: one shared sample is fine.

## UI contract (ux)
1. `/demo` renders the same tabs (Now, Activity with its Feed, Table and Map views, Money, Plan, Truck) against the sample vehicle id; drive and charge detail pages open under `/demo` too.
2. Persistent top banner: "Sample truck. Connect your Rivian to see yours" with a primary button to signup.
3. In-app, when a signed-in user has no vehicle yet: a card "Try with a sample truck" next to "Connect Rivian".
4. After connecting a real vehicle, the sample card disappears.
5. Empty states for a real new user (no data yet, first sync in progress) must look intentional and point to the sample.

## Acceptance
- Sample vehicle's monthly statement total equals the sum of its receipts (same invariant as prod).
- No request from `/demo` reaches the Rivian client (assert in test).
- Demo works with cookies cleared and JS storage blocked.
- Corpus generator output is byte-identical across runs for the same seed.

## Out of scope
Per-user fake data, personalization, editing the sample.
