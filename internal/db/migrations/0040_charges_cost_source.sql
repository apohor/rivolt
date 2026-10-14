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

-- reprice --apply keeps what it overwrote, so a re-price can be undone:
--   UPDATE charges SET cost = legacy_cost, price_per_kwh = legacy_price_per_kwh,
--          cost_source = NULL, network = NULL
--   WHERE legacy_cost IS NOT NULL;
ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS legacy_cost          NUMERIC(12,4),
    ADD COLUMN IF NOT EXISTS legacy_price_per_kwh NUMERIC(12,6);
