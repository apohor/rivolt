# Granting Pro at no charge to one account (`rivolt grant-pro`)

For the few accounts the owner offers a free Pro year (for example video creators). It writes one `entitlements` row with source `grant` and an end
date, and one `admin_audit` row, in one transaction. There is no admin-site button and no HTTP route for it on purpose: a command is smaller, leaves
a record, and adds no surface to attack.

## Running it

Run it where the founding command runs: inside a server pod, which already has the database settings. The operator (lead or owner) does this; an
agent never does, and nothing here needs new access.

```
kubectl -n rivolt exec deploy/rivolt -- /app/rivolt grant-pro --email creator@example.com --until 2027-11-21 --reason "creator program, year one" --dry-run
```

1. Dry run first (`--dry-run`). It prints the masked account, whether the address is verified, every entitlement row the account already has, and
   the plan. It writes nothing.
2. Run the same line without `--dry-run`. It prints the masked account, the end date and the audit row number.
3. The account reads as Pro within about 15 seconds (the plan cache).

Rules the command enforces, each with a message and exit code 1 (nothing written): exactly one account has that sign-in email; it is not the demo
account and not disabled; the end date is after today and at most 400 days ahead; the account has no running Stripe or Apple plan, and no running founding year (grant after it ends: its end-of-year notice would
be wrong), and no trial or earlier grant that already ends at or after the new end date. A flag mistake exits 2 before the database is opened.

The end date is inclusive: Pro lasts until the last second of that UTC day. After it the account is free again and nothing is deleted; history
older than the free window is hidden, not removed. If the account subscribes during a grant, Stripe takes precedence and billing starts when the grant
ends.

## Revoking

Same command with `--revoke` (dry run first). It ends only a running `grant` row; a paid or founding row is never touched, and a revoke with no running
grant is refused.

```
kubectl -n rivolt exec deploy/rivolt -- /app/rivolt grant-pro --revoke --email creator@example.com --reason "program ended" --dry-run
```

A revoke does not move the first charge of a subscription bought during the grant: its Stripe `trial_end` stays at the old grant end, so the account keeps
Pro and is first charged on that date.

## The record

Each real run adds an `admin_audit` row: action `entitlement.grant` or `entitlement.revoke_grant`, target the account, access label `cli:grant-pro`,
and details `{source, reason, until}`. The reason is one line of at most 200 characters; put no email address or other personal data in it (the command
refuses an `@`). To read them: `SELECT at, action, target_user_id, details FROM admin_audit WHERE action LIKE 'entitlement.%grant' ORDER BY id DESC`.

Grant rows send no billing email and no renewal notice: those jobs look at Stripe rows only.
