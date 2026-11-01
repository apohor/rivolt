-- 0105_apple_link_intents.sql: "Link Apple to my account" (design: apple-link-design.md, DA-approved Nov 1).
--
--   apple_link_intents   a signed-in user's request to attach an Apple identity to their own account. Apple answers the
--                        sign-in with a cross-site POST that carries no Rivolt session cookie, and the state cookie is
--                        not signed, so "link to whom" cannot come from the browser: it is kept here, written only by an
--                        authenticated start, keyed by the hash of the OAuth state, and consumed (deleted) by the one
--                        callback that presents that state. It also holds the hash of the Kratos session that started
--                        it, so a link started by one person on a shared browser cannot be completed after they sign out.
--
-- No Apple identifier, token, email or state is stored: only sha256 of the state and of the session id. A user has at
-- most one open intent (starting again replaces it). Rows live ten minutes and are swept after a day by retention. They
-- go with the account.
--
-- One new table. Rollback: DROP TABLE apple_link_intents;

CREATE TABLE IF NOT EXISTS apple_link_intents (
    state_hash   BYTEA       PRIMARY KEY,
    user_id      UUID        NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    session_hash BYTEA       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);

-- Sign-out deletes every open intent of the session that signed out.
CREATE INDEX IF NOT EXISTS apple_link_intents_session ON apple_link_intents (session_hash);
-- Retention sweep.
CREATE INDEX IF NOT EXISTS apple_link_intents_created_at ON apple_link_intents (created_at);

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS). The callback has no request
-- user, so it reads and consumes an intent on the system pool, like the Apple notification endpoint does; the policy
-- covers any request-pool access.
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['apple_link_intents'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
