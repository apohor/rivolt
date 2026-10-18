-- 0066_trip_plan_links_day.sql — plan vs actual per day of a multi-day trip.
--
-- A multi-day plan is one saved trip with several days, and the recorded
-- outing that carries it out is usually ONE ledger outing (leaving home to
-- getting back), so every day links to the same outing. A link therefore needs
-- to say which day it is for.
--
--   day_index  0 = the whole trip (single-day saved trips, and every row that
--              existed before this migration); 1..N = that day of a multi-day
--              trip. NOT NULL so it can sit in the primary key.
--
-- The primary key becomes (saved_trip_id, day_index, trip_id) and "at most one
-- linked outing" becomes "at most one per day". Existing rows are all day 0,
-- so nothing about them changes. The table is a handful of rows per user;
-- RLS (0059, 0060) is on the table and unaffected by a new column.

ALTER TABLE trip_plan_links
    ADD COLUMN IF NOT EXISTS day_index INTEGER NOT NULL DEFAULT 0 CHECK (day_index >= 0);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint
                WHERE conrelid = 'trip_plan_links'::regclass
                  AND conname = 'trip_plan_links_pkey'
                  AND pg_get_constraintdef(oid) NOT LIKE '%day_index%') THEN
        ALTER TABLE trip_plan_links DROP CONSTRAINT trip_plan_links_pkey;
        ALTER TABLE trip_plan_links ADD CONSTRAINT trip_plan_links_pkey
            PRIMARY KEY (saved_trip_id, day_index, trip_id);
    END IF;
END $$;

DROP INDEX IF EXISTS trip_plan_links_one_linked;
CREATE UNIQUE INDEX IF NOT EXISTS trip_plan_links_one_linked_per_day
    ON trip_plan_links (saved_trip_id, day_index) WHERE status = 'linked';
