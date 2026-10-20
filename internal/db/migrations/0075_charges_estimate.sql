-- 0075_charges_estimate.sql — a fast-charging session priced from a typical
-- public price (cost_source = 'observed_estimate') remembers which report the
-- figure came from, so the app can say "Typical price seen by DCFC Tracker,
-- Oct 2026" and a later receipt or the user's own rate can replace it.
--
-- estimate_month is the report month ("2026-10"); estimate_url is the
-- dcfctracker.com page. NULL on every other row. Metadata-only ADD COLUMNs:
-- no rewrite.

ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS estimate_month TEXT,
    ADD COLUMN IF NOT EXISTS estimate_url   TEXT;
