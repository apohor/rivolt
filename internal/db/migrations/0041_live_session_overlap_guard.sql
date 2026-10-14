-- 0041_live_session_overlap_guard.sql — one vehicle can't be in two live
-- charging (or driving) sessions at the same time.
--
-- Rationale:
--
--   Charges and drives are keyed by (vehicle_id, external_id), and the
--   live recorder's external_id embeds the session's start second. Two
--   recorders on one vehicle (the May 7-9 2026 multi-replica split-brain,
--   or any future lease bug) open the "same" session a second apart and
--   both rows land: prod has a 2h36m charge recorded twice (18.1 / 18.5
--   kWh) and three fragmented drives from that window. The ledger hides
--   such pairs at read time; this makes the database refuse new ones.
--
--   Exclusion constraints over (vehicle_id, tstzrange(started_at,
--   ended_at)) reject any second live row whose time range overlaps an
--   existing one for the same vehicle. Ranges are half-open, so a
--   session that starts exactly when the previous one ended is fine,
--   and a zero-length row is an empty range that overlaps nothing.
--   GREATEST(ended_at, started_at) keeps a (never observed, but
--   possible) ended-before-started row from raising a range error.
--
-- Scope:
--
--   - source = 'live' only. Imports aren't recorder output; overlap
--     between an import and live data is the importer's problem.
--   - started_at >= 2026-05-10 only. Rows from before the fix that
--     ended the split-brain (v0.17.146, 2026-05-09) include the known
--     duplicates; they are grandfathered rather than deleted here.
--     Cleaning them up is a separate, owner-approved data change, after
--     which a later migration can drop this cutoff. A read-only prod
--     audit (2026-10-13) found 0 live overlaps after the cutoff in 660
--     live charges and 1,918 live drives, so the constraint applies to
--     today's data as-is.
--
-- Safety on self-hosted installs:
--
--   btree_gist (contrib, trusted since PG13) supplies the uuid gist
--   operator class. If the extension can't be created, or an install's
--   own data already overlaps after the cutoff, the guard is skipped
--   with a WARNING instead of failing the migration and blocking boot.
--   The recorder treats a rejection (SQLSTATE 23P01) as "another
--   recorder owns this session" and stops writing that session.

DO $$
BEGIN
    BEGIN
        CREATE EXTENSION IF NOT EXISTS btree_gist;
    EXCEPTION WHEN insufficient_privilege OR undefined_file OR feature_not_supported THEN
        RAISE WARNING 'live session overlap guard not installed: btree_gist unavailable (%)', SQLERRM;
        RETURN;
    END;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'charges_live_no_overlap') THEN
        BEGIN
            ALTER TABLE charges ADD CONSTRAINT charges_live_no_overlap
                EXCLUDE USING gist (vehicle_id WITH =, tstzrange(started_at, GREATEST(ended_at, started_at)) WITH &&)
                WHERE (source = 'live' AND started_at >= TIMESTAMPTZ '2026-05-10 00:00:00+00');
        EXCEPTION WHEN exclusion_violation THEN
            RAISE WARNING 'charges_live_no_overlap not installed: existing live charges overlap (%)', SQLERRM;
        END;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'drives_live_no_overlap') THEN
        BEGIN
            ALTER TABLE drives ADD CONSTRAINT drives_live_no_overlap
                EXCLUDE USING gist (vehicle_id WITH =, tstzrange(started_at, GREATEST(ended_at, started_at)) WITH &&)
                WHERE (source = 'live' AND started_at >= TIMESTAMPTZ '2026-05-10 00:00:00+00');
        EXCEPTION WHEN exclusion_violation THEN
            RAISE WARNING 'drives_live_no_overlap not installed: existing live drives overlap (%)', SQLERRM;
        END;
    END IF;
END
$$;
