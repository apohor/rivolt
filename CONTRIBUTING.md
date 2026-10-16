# Contributing to Rivolt

Thanks for your interest. Rivolt is early-stage and moving fast, so contributor workflow is intentionally simple.

## Before opening a PR

1. **Open an issue first** for anything non-trivial. We'll agree on scope before you spend time.
2. **One concern per PR.** Smaller is easier to review.
3. **No AI-only PRs.** You're welcome to use AI to draft code, but you own the result — read it, test it, and be ready to defend every line.

## Local setup

See [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md).

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
