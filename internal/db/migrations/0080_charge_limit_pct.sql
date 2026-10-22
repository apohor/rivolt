-- 0080_charge_limit_pct.sql — the charge limit the truck was set to during a charge.
--
-- Stored on the charge row so the charts can draw it for any month: vehicle_state is
-- thinned and archived (N19), and a limit looked up there would vanish with the
-- samples. The recorder sets it from the same frames as the rest of the session; a
-- later save that carries none keeps the stored one. NULL = unknown.
--
-- The backfill takes, for each existing charge, the last positive limit reported
-- from the charge's start to five minutes after its end (1 to 100 only). Charges
-- whose samples are already gone stay NULL. Both vehicle_id and user_id are matched
-- so either of vehicle_state's indexes ((vehicle_id, at) primary key, (user_id, at))
-- serves each charge's lookup as a range scan. Run on prod's 912 charges, read-only,
-- it takes about 10 ms.
--
-- Rollback: ALTER TABLE charges DROP COLUMN charge_limit_pct;

ALTER TABLE charges ADD COLUMN IF NOT EXISTS charge_limit_pct DOUBLE PRECISION;

UPDATE charges c
   SET charge_limit_pct = (
        SELECT vs.charge_limit_pct
          FROM vehicle_state vs
         WHERE vs.vehicle_id = c.vehicle_id
           AND vs.user_id = c.user_id
           AND vs.at >= c.started_at
           AND vs.at <= c.ended_at + interval '5 minutes'
           AND vs.charge_limit_pct BETWEEN 1 AND 100
         ORDER BY vs.at DESC
         LIMIT 1)
 WHERE c.charge_limit_pct IS NULL;
