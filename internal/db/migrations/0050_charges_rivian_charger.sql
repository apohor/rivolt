-- 0050_charges_rivian_charger.sql — whether Rivian said the session ran on
-- one of its own chargers.
--
-- Rivian's getLiveSessionHistory reports isRivianCharger once per
-- session (the recorder fetches it when charging starts). It is the only
-- signal we get that a Rivian-billed fast charge was on the Rivian
-- Adventure Network rather than another network billed through the
-- Rivian app, so the recorder stores it and names the network from it.
--
--   NULL   not reported (WS-only session, REST fetch failed, legacy rows)
--   true   Rivian's charger (RAN fast charger, Waypoints, Wall Charger)
--   false  another network's charger
--
-- Metadata-only ADD COLUMN (nullable, no default): no rewrite, a brief
-- lock. No backfill.

ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS rivian_charger BOOLEAN;
