# Contributing to Rivolt

Thanks for your interest. Rivolt is early-stage and moving fast, so contributor workflow is intentionally simple.

## Before opening a PR

1. **Open an issue first** for anything non-trivial. We'll agree on scope before you spend time.
2. **One concern per PR.** Smaller is easier to review.
3. **No AI-only PRs.** You're welcome to use AI to draft code, but you own the result — read it, test it, and be ready to defend every line.

## Local setup

Go: `go test ./internal/<package>/...` for the package you changed. Web: `cd web && npm ci && npm run dev` (Vite dev server), `npm run lint`, and `npx vitest run <files>`.

## Web changes and e2e

- **No stacked PRs.** One PR off `main`; CI only runs against `main`, so a stacked branch gets no checks.
- Unit tests: `cd web && npx vitest run <the files you changed>`.
- Playwright locally: build once (`npm run build`; the specs run against `vite preview`), then run only the spec you changed, one worker: `npx playwright test <spec> --project=chromium --workers=1`. Don't run the whole suite on a laptop; CI does.
- CI (`e2e` job, web PRs only) runs the `ui-next-*` specs on three Chromium shards plus one WebKit shard (the iPhone Safari engine, phone specs), and a `legacy` suite (every other spec, flag off) on two shards. **`e2e-ok` is the required check**; it is green when the matrix passed or was skipped (a PR that touches no web code).
- The visual-regression suite (`ui-next-visual`) is opt-in: it runs only with `E2E_VISUAL=1`, and baseline changes need a reviewer who is not the author.
- A flaky or blocked spec goes in [`web/e2e/QUARANTINE.md`](web/e2e/QUARANTINE.md) (at most 3, each with an owner and a date) and is marked `test.fixme` with a comment pointing at that file. A test enforces the cap and the reference.

## Code style

- Go: `gofmt` + `go vet`. Standard library first; minimal third-party deps.
- TypeScript: `prettier` + strict mode. Tailwind for styling. No CSS-in-JS.
- Commit messages: conventional-ish. `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`.

## Scope

**In scope:**
- Rivian data ingestion, analytics, dashboards
- Home-energy integrations
- AI coach prompts/adapters
- Overland / trip-journal features
- Self-hosted deployment tooling

**Out of scope:**
- Social features (leaderboards, friend lists)
- Third-party telemetry / analytics on Rivolt itself
- Anything that sends user data off-device without explicit consent

## Security

If you find a security issue, please email the maintainer privately instead of opening a public issue. See [`SECURITY.md`](SECURITY.md).

## Legal

By submitting a PR you agree to license your contribution under the MIT License (for core) or the then-current commercial license (for premium plugins, clearly marked).

"Rivian" is a trademark of Rivian Automotive, Inc. Rivolt is unaffiliated.
