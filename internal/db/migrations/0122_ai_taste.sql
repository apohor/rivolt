-- 0122_ai_taste.sql — the free account's AI taste (Pro v6): a few operator-key answers a month.
--
--   1. ai_taste_usage: one row per (user, UTC calendar month), n = answers taken. Written on the request pool under
--      row-level security, like ai_call_usage (0033); it goes with the account (ON DELETE CASCADE).
--   2. ai_taste_spend: the install-wide ceiling on what free-taste calls may cost the operator in a UTC day, in micro-dollars.
--      One row per day, no user column; only the system pool writes it (appDeniedTables keeps the request role out).

SET LOCAL lock_timeout = '2s';

CREATE TABLE IF NOT EXISTS ai_taste_usage (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    month   DATE NOT NULL,
    n       INT  NOT NULL DEFAULT 0 CHECK (n >= 0),
    PRIMARY KEY (user_id, month)
);

ALTER TABLE ai_taste_usage ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_taste_usage_tenant_isolation ON ai_taste_usage;
CREATE POLICY ai_taste_usage_tenant_isolation ON ai_taste_usage
    USING (user_id = rivolt_current_user_id())
    WITH CHECK (user_id = rivolt_current_user_id());

CREATE TABLE IF NOT EXISTS ai_taste_spend (
    day       DATE   PRIMARY KEY,
    micro_usd BIGINT NOT NULL DEFAULT 0 CHECK (micro_usd >= 0)
);
