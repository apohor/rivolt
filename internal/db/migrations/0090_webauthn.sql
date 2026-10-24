-- 0090_webauthn.sql: passkeys (WebAuthn) for signing in to Rivolt's own email accounts (Sprint 10; design DA-approved
-- Oct 24).
--
--   webauthn_users        the account's WebAuthn user handle: random, opaque, the same for all of the account's passkeys
--                         (the authenticator stores it; it is not the Rivolt user id).
--   webauthn_credentials  one row per passkey: its public key and credential id (public values), the signature counter,
--                         the transports and AAGUID the authenticator reported, whether it can be backed up (synced) and
--                         whether it is, a label the user gives it, and when it was made and last used. No private key
--                         exists on the server.
--   webauthn_ceremonies   the state of a registration or sign-in in progress (the challenge): single use, five minutes,
--                         swept by retention. A sign-in ceremony is bound to its Hydra login challenge.
--
-- All of it goes with the account (ON DELETE CASCADE). Tenant isolation like every table with a user_id.
--
-- Rollback: DROP TABLE webauthn_ceremonies, webauthn_credentials, webauthn_users;

CREATE TABLE IF NOT EXISTS webauthn_users (
    user_id    UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    handle     BYTEA       NOT NULL UNIQUE CHECK (octet_length(handle) = 32),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS webauthn_credentials (
    id              UUID        PRIMARY KEY,
    user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id   BYTEA       NOT NULL UNIQUE,
    public_key      BYTEA       NOT NULL,
    sign_count      BIGINT      NOT NULL DEFAULT 0,
    transports      TEXT        NOT NULL DEFAULT '',   -- comma separated: usb, nfc, ble, internal, hybrid
    aaguid          BYTEA,
    backup_eligible BOOLEAN     NOT NULL DEFAULT FALSE,
    backup_state    BOOLEAN     NOT NULL DEFAULT FALSE,
    label           TEXT        NOT NULL DEFAULT 'Passkey' CHECK (char_length(label) BETWEEN 1 AND 60),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS webauthn_credentials_user_idx ON webauthn_credentials (user_id);

CREATE TABLE IF NOT EXISTS webauthn_ceremonies (
    id              UUID        PRIMARY KEY,
    purpose         TEXT        NOT NULL CHECK (purpose IN ('register', 'login')),
    user_id         UUID        REFERENCES users(id) ON DELETE CASCADE,
    hydra_challenge TEXT,
    session         BYTEA       NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS webauthn_ceremonies_expires_idx ON webauthn_ceremonies (expires_at);

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['webauthn_users', 'webauthn_credentials', 'webauthn_ceremonies'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
