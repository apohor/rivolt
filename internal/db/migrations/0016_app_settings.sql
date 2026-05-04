-- 0016_app_settings.sql
--
-- Install-wide configuration table. Distinct from `user_settings`
-- (per-user KV via internal/settings) and `flags` (boolean
-- operational toggles via internal/flags).
--
-- Today this stores AI provider keys + default models; later it
-- will hold global feature flags that need an API surface, install
-- branding, etc.
--
-- Values are envelope-encrypted at the application layer via
-- internal/secrets.Sealer (RIVOLT_KEK), so a bare `pg_dump` does
-- NOT exfiltrate the keys. The column is BYTEA to make that
-- contract explicit.

CREATE TABLE IF NOT EXISTS app_settings (
  key        TEXT  PRIMARY KEY,
  value      BYTEA NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
