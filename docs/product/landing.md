# Landing page: structure and copy (P2)

Audience: segments A (home charger) and B (road-tripper) first, C (self-hoster) second. See the PM report for jobs-to-be-done.
Constraint: no claim about Rivian's app (see claims-verification.md). No "battery-friendly" or "real-time" headline until the drain gate passes.
Page lives at `/` for signed-out visitors, plus `/demo` (public, no login).

## Structure
1. Hero: headline, one-line sub, two buttons (See the demo / Connect your Rivian), a Money statement screenshot.
2. Three claims, one block each, each with a real screenshot.
3. "How your Rivian login is handled" (trust block).
4. Open source and self-host strip.
5. Honest limits ("What Rivolt can't do").
6. FAQ, footer (license, trademark disclaimer, ToS, Privacy).

## Copy (draft)
**Hero**
- H1: What your Rivian actually costs.
- Sub: Every charge and trip priced at your rates, or flagged when we can't price it, with the source of every number. Open source. Self-host it or let us run it.
- Buttons: *Try the demo truck* · *Connect my Rivian*
- Under buttons: Free while in beta. Invite-only.

**Claim 1: Every dollar has a source**
Billed by Rivian, estimated from your home rate, estimated from the charging network, or entered by you. Charges we can't price are flagged, not guessed. You always see which one.
*Screenshot: receipts with source labels.*

**Claim 2: Price the trip, then check yourself**
See DCFC spend versus charging at home before you leave. After the trip, see planned versus actual.
*Screenshot: Plan summary + Activity trip.* (Plan-vs-actual is slice 8: ship the claim only when live.)

**Claim 3: Yours to keep**
MIT licensed. Download all your data as a JSON backup (CSV statement export is planned; don't claim it until shipped). Run it on your own server with docker compose and an OIDC provider (maps and planner need the optional tiles and Valhalla services), or use ours.
*Link: GitHub, self-host guide.*

**Trust block**
- We store your Rivian session encrypted; your password is never stored. (Verify against the code before publishing.)
- We recommend a separate Authorized Driver account. Step-by-step guide linked.
- With an Authorized Driver account you can revoke Rivolt from the Rivian app at any time. Either way you can delete your data from Settings.

**Honest limits**
- Unofficial: Rivolt uses Rivian's unofficial API and is not affiliated with Rivian. It can break.
- No fleet leaderboards or software-rollout tracker: use Roamer for those.
- Beta: fewer than ten vehicles today; expect rough edges. (Update the number at publish time; don't hard-code.)

## Live-demo idea
`/demo` shows a generated truck ("Sample R1S") through the real Money, Now and Activity screens, read-only, no login, no Rivian call. Banner: "Sample data". Spec: sample-truck.md.

## Metrics
Visitor to demo view, demo view to connect click, connect click to connected vehicle. See instrumentation.md.
