-- 0040_charges_cost_source.sql — record where a charge's price came from.
--
-- Rationale:
--
--   The recorder priced every session Rivian didn't bill at the user's
--   home rate, including Tesla / Electrify America fast charges, so the
--   ledger read roughly half the real spend. Sessions are now priced by
--   source, and the source is stored so the UI can tell billed from
--   estimated:
--
--     rivian            Rivian-reported price (RAN, Wall Charger)
--     home_rate         user's home $/kWh (slow charging)
--     network_estimate  DCFC: nearest NREL station's network rate
--     user              price entered by the user
--
--   network holds the matched DCFC network's display name.
--
--   Backfill: none here. Legacy rows stay NULL (the ledger derives
--   their source at read time); `rivolt reprice` re-prices the DC fast
--   charges stored at the home rate, per install.

ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS cost_source TEXT,
    ADD COLUMN IF NOT EXISTS network     TEXT;

-- reprice --apply keeps what it overwrote (0 for "no price"), so a
-- re-price can be undone without touching rows the recorder priced:
--   UPDATE charges SET cost = NULLIF(legacy_cost, 0),
--          price_per_kwh = NULLIF(legacy_price_per_kwh, 0),
--          cost_source = NULL, network = NULL,
--          legacy_cost = NULL, legacy_price_per_kwh = NULL
--   WHERE legacy_cost IS NOT NULL;
ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS legacy_cost          NUMERIC(12,4),
    ADD COLUMN IF NOT EXISTS legacy_price_per_kwh NUMERIC(12,6);
