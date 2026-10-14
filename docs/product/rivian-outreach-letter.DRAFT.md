# Draft outreach to Rivian developer relations

> DRAFT for the owner to edit and send. Not sent. **[DECIDE]** who signs, and from which address. Keep it short: aim for under one page.
> Rationale: the ROADMAP lists dev-relations outreach as unchecked before opening signups. Rivian's CSO has said he expects "APIs and an ecosystem of partners" (TechCrunch, 2024-11-05) and has named TezLab as an early partner; this letter asks for the path, not a favour.

**To:** developer relations / partnerships (address **[VERIFY: find via rivian.com contact or LinkedIn; no public developer portal exists]**)
**Subject:** Open-source Rivian cost-ledger app: request for a sanctioned API or partner path

Hello,

I'm the developer of Rivolt (rivolt.dev, open source under MIT, github.com/apohor/rivolt), an independent companion app for R1T/R1S owners. It shows what a Rivian actually costs: every charge and trip priced at the owner's own rates, with the source of each price, a monthly statement and CSV export, and a trip-cost planner. It is small (a few vehicles in beta) and not affiliated with Rivian.

**How it works today, in the open.** Rivolt uses the same interfaces as the Rivian mobile app, reverse-engineered, because no public API exists. I'd prefer to move to a sanctioned path, which is why I'm writing. I want to be straightforward about what it does:
- **Read-only.** It reads vehicle state and history. It sends no vehicle commands. **[VERIFY before sending]**
- **Credentials.** Users sign in with their own Rivian account (we recommend a dedicated Authorized Driver account). Passwords are never stored; session tokens are encrypted at rest with per-user keys. Users can revoke at any time from the Rivian app.
- **Gentle on your systems.** Outbound calls pass through a token bucket (60-call burst, 2 requests/s sustained; 1/s for sign-in); a circuit breaker stops on repeated failures; a sleep gate parks non-essential subscriptions while the vehicle is asleep so it can sleep; and an operator kill switch can pause all Rivian traffic in seconds. Upstream errors are classified (rate-limited, outage, user-action) and rate limits back off rather than retry.
- **Honest to users.** The app says it is unofficial and may break. **[VERIFY after ToS is published]**

**What I'm asking.**
1. Is there, or will there be, a sanctioned way for a third-party app to read owner-authorised vehicle data (OAuth, scoped, rate-limited)? What would a partner or pilot path require (entity, security review, minimum scale, fees)?
2. If not yet, would you tell me which behaviours would cause concern, so I can avoid them (call rates, subscription topics, user-agent handling)?
3. Is there a contact I can notify if I see unexpected rate-limiting that might affect owners' official-app experience?

**What I can offer.** Auditable open-source credential handling, a feature set that complements rather than duplicates the official app (cost history, plan-vs-actual, export), and willingness to move to an official API as soon as one exists and to stop unofficial access when it does.

Happy to show a demo or share a technical write-up.

Thank you,
**[Name]** · **[email]** · rivolt.dev

## Notes for the sender
- Send *after* ToS/Privacy are published and the drain gate is met; send before charging.
- Do not claim partnership, endorsement or "official" anything.
- Keep a copy of any reply: Gate C asks for "contact made or an explicit decision to stay quiet".
- Precedent (public, partial): TechCrunch 2024-11-05 names TezLab as an early partner; terms are not public **[unverified]**.
