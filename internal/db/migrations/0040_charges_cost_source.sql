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
--   Backfill: none here. Legacy rows stay NULL; `rivolt reprice`
--   classifies and (with --apply) re-prices them per install.

ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS cost_source TEXT,
    ADD COLUMN IF NOT EXISTS network     TEXT;
