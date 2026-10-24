-- 0086_apple_identities.sql: Sign in with Apple (App Store 4.8; architect's design, DA-approved Oct 24).
--
--   apple_identities   which Rivolt account an Apple sign-in identity (its stable `sub`) belongs to. Apple's
--                      server-to-server notifications (consent revoked, Apple account deleted, relay email off or on)
--                      name the user only by `sub`, so the server needs this to act on them; nothing else. One row
--                      per account at most, and one account per `sub`. Goes with the account.
--
-- The refresh token Apple issues (kept only to revoke it when the account is deleted) is not here: it's in
-- user_secrets as "apple.refresh_token", sealed like the Rivian session, bound to the user. Never the access token
-- or the id token. No Apple identifier is logged.
--
-- One new table. Rollback: DROP TABLE apple_identities;

CREATE TABLE IF NOT EXISTS apple_identities (
    user_id    UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    sub        TEXT        NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['apple_identities'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
