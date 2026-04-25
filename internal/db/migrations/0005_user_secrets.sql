-- 0005_user_secrets.sql — per-user encrypted blob store.
--
-- Holds any credential material that we don't want in plaintext
-- on disk: Rivian session tokens (first customer), AI provider
-- API keys, push-subscription private keys, OIDC client secrets
-- once those land. See [`internal/crypto`](../../crypto) for the
-- envelope-encryption scheme and [`internal/secrets`](../../secrets)
-- for the store that writes to this table.
--
-- Rationale for a separate table (rather than sealed columns on
-- `users`):
--
--   - Keeps the hot `users` row small. `users` is read on
--     every authenticated request (needs_reauth, username, ...);
--     large BYTEA blobs on it would bloat the TOAST threshold.
--   - Lets a future read-only replica role be denied access to
--     this table while still serving the rest of the schema.
--   - ON DELETE CASCADE gives clean GDPR-delete semantics: drop
--     the user row, every secret under that user goes with it.
--   - Rotation tooling can walk this table with a single query
--     (`WHERE kek_id = '<old>'`) and re-wrap rows in batches.

CREATE TABLE IF NOT EXISTS user_secrets (
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    -- ciphertext is the full envelope blob produced by
    -- crypto.Sealer.Seal (magic + kek_id + nonces + wrapped DEK
    -- + GCM-sealed payload). We do not split the fields out into
    -- columns — the blob is opaque from the DB's perspective, and
    -- mixing SQL-layer parsing with the in-process envelope
    -- format would couple the schema to the crypto package's
    -- wire layout.
    ciphertext  BYTEA       NOT NULL,
    -- kek_id duplicates what's already in the blob header but
    -- exposes it as a queryable column. Rotation reads it to
    -- find rows written under a retired KEK without having to
    -- decode every blob.
    kek_id      TEXT        NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, name)
);

-- Index on kek_id for the rotation query. Small table, small
-- index; worth having from day one so the first rotation doesn't
-- seq-scan every user.
CREATE INDEX IF NOT EXISTS user_secrets_kek_id_idx ON user_secrets (kek_id);
