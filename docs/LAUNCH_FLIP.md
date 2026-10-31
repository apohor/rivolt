# Rivolt Pro: the launch flip

How the paid plan goes live on a deployment, in order, and how to take it back. Billing is off by default (`RIVOLT_BILLING` unset or
false): every account is Pro, no entitlement is read and nothing is sold. This is the procedure for turning it on.

Nothing here is switched on by a command that only reports. `rivolt launch-preflight` reads; `rivolt entitlements-founding` and
`rivolt launch-notice` are dry runs unless given `--apply`; the flip itself is one setting, last.

All `rivolt ...` commands run where the server's settings and database are (an app pod), with the same environment the server has.

## 0. Pre-flight (read-only)

```
rivolt launch-preflight --launch <instant>     # RFC3339; the instant the founding grant counts accounts up to (default: now)
```

It prints, and exits 0 only when nothing stops the flip (1 otherwise, 2 for bad flags):

- the launch-readiness list (`internal/launch`): items cleared and any still pending. The server refuses `RIVOLT_BILLING=true` while one
  is pending, so a pending item is a problem here too;
- the founding grant's dry-run counts: accounts, the demo, eligible (created by the launch instant, demo excluded), already granted, to
  grant. Accounts created after the instant are counted apart and warned about;
- the Terms and notice pages the emails link to (`/terms`, the previous Terms, `/privacy`, the Plan card and the export page), each
  answering 200;
- which billing settings are present, by name only, never a value: the Stripe secret key (and its mode, live or test), webhook secret and
  both prices, the public base URL, tax on or off, the StoreKit bundle, products and environment. A Stripe mode that does not match the
  Apple environment is a warning.

Run it again after every step below. `--offline` skips the page checks, `--no-db` skips the founding counts; both say so in the report.
The report holds counts, names and status codes only, so it can be pasted into the release thread.

## 1. Founding grant

Every account that exists at launch gets Pro free for a year from launch (no card, nothing auto-charges).

```
rivolt entitlements-founding --launch <instant>                                   # dry run: counts and a hash of the ids
rivolt entitlements-founding --launch <instant> --apply --expect-count N --expect-sha <hash>
```

`--apply` needs the count and hash from the dry run, so the run that applies is the run that was reviewed. It grants in one transaction,
checks that founding rows = accounts at launch minus the demo, and rolls back if not. Re-running grants nothing. Check the count
against the pre-flight's "to grant" before applying, and run the pre-flight again: "to grant" should now read 0.

## 2. Notices

The Terms section 11 notice (a free tier and Pro exist) goes to every account on or before the launch date, in the app and by email:

```
rivolt launch-notice --launch-date YYYY-MM-DD --test-to <your own user id>        # one email to you, writes nothing
rivolt launch-notice --launch-date YYYY-MM-DD                                      # dry run: counts only
rivolt launch-notice --launch-date YYYY-MM-DD --apply
```

The founding-year-end notices (30 days before each year ends) and the renewal and payment-failed emails are sent by the server's
own jobs; there is nothing to run for them.

## 3. Billing on

Only after 0 to 2 are clean, DA's compliance sheet is signed and the release gate (qa) has passed on the build that is deployed:

1. Set `RIVOLT_BILLING=true` for the deployment (the chart values) and roll out. The server refuses to start if the launch-readiness
   list has a pending item; that refusal is the last guard, not a plan.
2. Run `rivolt launch-preflight` once more: "RIVOLT_BILLING: on", still READY.
3. Check as a free test account and as a founding one: `GET /api/auth/me` reports `billing_enabled: true` and the right plan; the Plan
   card shows the free-period wording for a founding account.

## Rollback

Set `RIVOLT_BILLING` back to false (or unset it) and roll out. With billing off every account is Pro again and no entitlement is read, so
everything is open at once and **nothing is deleted**:

- founding grants, entitlement rows and Stripe and Apple subscription records stay as they are, and a later flip finds them again;
- people who subscribed keep their subscriptions at Stripe and Apple: pause or refund those there if the rollback should reach them
  (their data is not touched by the flag);
- notices and emails already sent stay sent.

Do not delete founding or entitlement rows to roll back; the flag is the rollback. If a step fails before the flip (0 to 2), nothing needs
undoing: the founding grant is idempotent and harmless with billing off, and a notice that failed to send is retried by running the same
command again.
