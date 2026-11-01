<!-- What changes and why, in a few lines. -->

### Replicas (docs/ARCHITECTURE.md, Decision 13)
Tick the first line, or every other line that applies:
- [ ] This PR adds no per-user or install-wide state held in process memory, no rate limit and no scheduled job.
- [ ] It adds in-memory state: Postgres is the source of truth, every replica refreshes it (versioned read, optionally NOTIFY), dependent writes are version-conditional, it's dropped on account deletion, and a 3-instance integration test covers it.
- [ ] It adds a rate limit or a scheduled job: limits that bound abuse or cost are shared (Redis/Postgres, with a per-process fallback); jobs and emails run once cluster-wide (claim or advisory lock).

### Policy pages
CI (`policy-dates`) fails a PR that changes the text of `web/src/content/privacy.md` or `terms.md` without moving its "Last updated" date forward, in the same PR. The frozen October 23, 2026 Terms (`terms-2026-10-23.md`, served at `/terms/2026-10-23`) never change. If you change policy text, put the merge date at the top and say so here.
