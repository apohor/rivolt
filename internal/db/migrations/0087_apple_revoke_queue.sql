-- 0087_apple_revoke_queue.sql: Sign in with Apple revocation retries (architect's design; DA-approved Oct 24).
--
-- Deleting an account revokes its Sign in with Apple refresh token at Apple before the account rows go. When Apple
-- can't be reached, the revoke is queued in idp_delete_failures (the queue Kratos deletions already use) as a
-- kind = 'apple' row:
--
--   username   'apple:' || the deleted account's id (never an email or the Apple sub), so the existing unique
--              index on lower(username) keeps one row per deleted account
--   sealed     the refresh token, sealed by the KEK envelope with seal_uid as associated data; it doesn't depend
--              on anything stored with the user, so it still opens after the account cascade
--   seal_uid   the deleted account's id (the AAD)
--
-- A row goes when the revoke succeeds, or after 30 days or 20 attempts, whichever comes first (the retrier).
-- Existing rows become kind = 'kratos'.
--
-- Additive. Rollback: DELETE FROM idp_delete_failures WHERE kind = 'apple';
--   ALTER TABLE idp_delete_failures DROP CONSTRAINT idp_delete_failures_kind_check,
--   DROP COLUMN seal_uid, DROP COLUMN sealed, DROP COLUMN kind;

ALTER TABLE idp_delete_failures
    ADD COLUMN IF NOT EXISTS kind     TEXT  NOT NULL DEFAULT 'kratos',
    ADD COLUMN IF NOT EXISTS sealed   BYTEA,
    ADD COLUMN IF NOT EXISTS seal_uid UUID;

DO $$
BEGIN
    ALTER TABLE idp_delete_failures ADD CONSTRAINT idp_delete_failures_kind_check CHECK (
        (kind = 'kratos' AND sealed IS NULL AND seal_uid IS NULL)
        OR (kind = 'apple' AND sealed IS NOT NULL AND seal_uid IS NOT NULL AND username = 'apple:' || seal_uid::text)
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
