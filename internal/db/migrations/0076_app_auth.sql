-- 0076_app_auth.sql — native app sign-in (iOS design §2, DA-approved).
--
-- The app signs in through the server's own web sign-in: it opens
-- /api/auth/app/start with a PKCE challenge, the browser completes the usual
-- OIDC sign-in, /api/auth/app/finish mints a one-time code for the signed-in
-- user and redirects to the app's fixed callback, and the app exchanges the
-- code plus its PKCE verifier at /api/auth/app/token for a session token.
--
-- sessions.kind: 'web' (the browser cookie) or 'app' (a bearer token held by
-- the iPhone app). Same table, same revocation path.
--
-- app_auth_requests: one row per sign-in attempt, alive for minutes. Only
-- HMACs of the browser-binding state value and of the one-time code are
-- stored (same pepper as session tokens), never the raw values.
--
-- Rollback: DROP TABLE app_auth_requests (drops its policy); ALTER TABLE sessions DROP COLUMN kind.

ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'web'
    CHECK (kind IN ('web', 'app'));

CREATE TABLE IF NOT EXISTS app_auth_requests (
    state_hash      BYTEA PRIMARY KEY,
    challenge       TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    user_id         UUID REFERENCES users(id) ON DELETE CASCADE,
    code_hash       BYTEA UNIQUE,
    code_expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS app_auth_requests_expires_at_idx
    ON app_auth_requests (expires_at);

-- Tenant row-level security, the 0008/0059 stage-1 pattern: ENABLE, not
-- FORCE, so the app role (the table owner) is unaffected and the policy is
-- ready for the role split. Like sessions, this table is read before any user
-- is known (start and token run unauthenticated), so the role-split stage
-- will need the same pre-auth access path sessions gets.
ALTER TABLE app_auth_requests ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS app_auth_requests_tenant_isolation ON app_auth_requests;
CREATE POLICY app_auth_requests_tenant_isolation ON app_auth_requests
    USING (user_id = rivolt_current_user_id())
    WITH CHECK (user_id = rivolt_current_user_id());
