-- 0043_live_session_overlap_guard_all_time.sql: the live-session overlap
-- guard (0041) now covers every live row, not just rows from 2026-05-10.
--
-- Rationale:
--
--   0041 exempted rows that started before 2026-05-10, because prod still
--   held the May 7-9 split-brain duplicates. The owner approved deleting
--   them (3 charges, 3 drives); a read-only re-audit on 2026-10-14 found
--   0 overlapping live charges and 0 overlapping live drives at any date,
--   on prod and on preview. So the exemption has nothing left to protect.
--
-- How:
--
--   For each table, create the all-time constraint under a temporary name,
--   then drop 0041's and rename. If an install still has pre-cutoff
--   overlaps (self-hosted, not cleaned up), creating it fails, the WARNING
--   says so, and 0041's cutoff constraint stays in place. If 0041 was
--   skipped (btree_gist unavailable), creating the gist constraint fails
--   the same way and is skipped too. Boot never fails here.

DO $$
BEGIN
    BEGIN
        ALTER TABLE charges ADD CONSTRAINT charges_live_no_overlap_all
            EXCLUDE USING gist (vehicle_id WITH =, tstzrange(started_at, GREATEST(ended_at, started_at)) WITH &&)
            WHERE (source = 'live');
        ALTER TABLE charges DROP CONSTRAINT IF EXISTS charges_live_no_overlap;
        ALTER TABLE charges RENAME CONSTRAINT charges_live_no_overlap_all TO charges_live_no_overlap;
    EXCEPTION WHEN exclusion_violation OR undefined_object THEN
        RAISE WARNING 'charges_live_no_overlap left as before (cutoff kept or guard absent): %', SQLERRM;
    END;

    BEGIN
        ALTER TABLE drives ADD CONSTRAINT drives_live_no_overlap_all
            EXCLUDE USING gist (vehicle_id WITH =, tstzrange(started_at, GREATEST(ended_at, started_at)) WITH &&)
            WHERE (source = 'live');
        ALTER TABLE drives DROP CONSTRAINT IF EXISTS drives_live_no_overlap;
        ALTER TABLE drives RENAME CONSTRAINT drives_live_no_overlap_all TO drives_live_no_overlap;
    EXCEPTION WHEN exclusion_violation OR undefined_object THEN
        RAISE WARNING 'drives_live_no_overlap left as before (cutoff kept or guard absent): %', SQLERRM;
    END;
END
$$;
