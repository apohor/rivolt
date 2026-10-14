-- 0046_events.sql — first-party product events (docs/product/instrumentation.md).
--
-- Privacy: counts and enums only. No IP address, no User-Agent, no
-- coordinates, emails, VINs or amounts are stored; the API allowlists
-- event names and property keys. anon_id is a random per-browser id
-- (disclosed in the Privacy Policy); user_id is set only when the
-- request carried a session. Rows go with the account (CASCADE) and
-- are purged after 90 days by the events writer.
--
-- Written by the server pool only (no read API), so no RLS policy:
-- anonymous inserts have no user context to key one on.

CREATE TABLE IF NOT EXISTS events (
    id      BIGSERIAL   PRIMARY KEY,
    ts      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    anon_id TEXT        NOT NULL,
    user_id UUID        REFERENCES users(id) ON DELETE CASCADE,
    name    TEXT        NOT NULL,
    props   JSONB       NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS events_ts ON events (ts);
CREATE INDEX IF NOT EXISTS events_user_name_ts ON events (user_id, name, ts) WHERE user_id IS NOT NULL;

-- North star: a user who viewed a priced receipt (receipt_open) within
-- 7 days of first connecting a Rivian account (rivian_connect_done).
-- Per user: events carry no vehicle ids.
CREATE OR REPLACE VIEW activated AS
SELECT c.user_id,
       c.connected_at,
       MIN(r.ts) AS activated_at
FROM (
    SELECT user_id, MIN(ts) AS connected_at
    FROM events
    WHERE name = 'rivian_connect_done' AND user_id IS NOT NULL
    GROUP BY user_id
) c
JOIN events r
  ON r.user_id = c.user_id
 AND r.name = 'receipt_open'
 AND r.ts >= c.connected_at
 AND r.ts <  c.connected_at + INTERVAL '7 days'
GROUP BY c.user_id, c.connected_at;
