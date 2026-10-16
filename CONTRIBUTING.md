# Contributing to Rivolt

Thanks for your interest. Rivolt is early-stage and moving fast, so contributor workflow is intentionally simple.

## Before opening a PR

1. **Open an issue first** for anything non-trivial. We'll agree on scope before you spend time.
2. **One concern per PR.** Smaller is easier to review.
3. **No AI-only PRs.** You're welcome to use AI to draft code, but you own the result — read it, test it, and be ready to defend every line.

## Local setup

See [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md).

## How changes land

- **No stacked PRs.** Branch from `main`, and keep one concern per PR. If
  B needs A, merge A first and then branch B from `main`. Unfinished UI
  goes behind `ui_theme_next` (see `docs/ARCHITECTURE.md`, "Web UI"),
  not into a branch stack.
- **Required checks:** `go`, `integration`, `web` and `e2e-ok`. Go and
  integration are skipped on web-only PRs.
- **Merging.** After the assigned reviewer approves a specific SHA, the
  author arms auto-merge on exactly that SHA:
  `gh pr merge N --auto --merge --match-head-commit <sha>`. A later push
  then needs a new approval. Changes to migrations, auth, the anonymous
  demo endpoints, `cmd/`, `deploy/` or `.github/` are merged by the
  maintainer after a security review.

## End-to-end tests (Playwright)

Specs live in `web/e2e/*.pw.ts`. Next-look specs are named `ui-next-*`.

**Locally.** Run only the spec you changed, with one worker:

```sh
cd web
npm run build
npx playwright test e2e/<your-spec>.pw.ts --workers=1
E2E_WEBKIT=1 npx playwright test e2e/<your-spec>.pw.ts --project=webkit --workers=1   # iPhone engine
```

The suite stubs the API, so no backend is needed. Don't run the whole
suite locally; CI does that.

**In CI** the `e2e` job runs on web PRs as a matrix:
- `next`: the `ui-next-*` specs, as 3 Chromium shards plus 1 WebKit shard
  (phone specs only).
- `legacy`: all other specs, as 2 Chromium shards.

`e2e-ok` sums up the matrix and is the required check, so a failed shard
blocks the merge. Each shard uploads its report, screenshots and traces
as an artifact (`e2e-<suite>-<project>-<shard>`, kept 7 days). Reviewers
open the screenshots rather than trusting a green tick. CI retries a
failed test once.

**Quarantine.** A flaky spec can be quarantined only through
`web/e2e/QUARANTINE.md`. There are at most 3 entries; each has an owner,
a tracking issue and a date. The spec is marked `test.fixme(...)`, not
deleted. The same file describes the release tag gate (iPhone WebKit on
the exact preview build).

**Visual baselines** are opt-in: `E2E_VISUAL=1` (`npm run e2e:visual`)
runs `ui-next-visual.pw.ts`. It is not part of the PR matrix. A baseline
update needs a reviewer who is not its author.

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
