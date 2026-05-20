-- 0032_vehicle_pack_health_samples.sql — derived effective pack
-- capacity per qualifying charge session.
--
-- Rivian doesn't expose SoH (verified against the 3.12 APK). We
-- compute an "effective pack kWh" ourselves from the SoC delta +
-- energy delivered of every clean charge session and store the
-- result here so the UI can plot a trend without re-deriving on
-- every page load.
--
-- One row per (vehicle, charge) when the charge qualifies. Charges
-- that don't qualify (small SoC delta, missing energy, station
-- derated, fault state) simply don't produce a row.
--
-- The fit:
--   pack_kwh_effective = energy_added_kwh / (soc_delta_pct / 100)
-- with soc_delta_pct = end_soc_pct - start_soc_pct.

CREATE TABLE IF NOT EXISTS vehicle_pack_health_samples (
    vehicle_id           UUID        NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    charge_id            UUID        NOT NULL REFERENCES charges(id) ON DELETE CASCADE,
    at                   TIMESTAMPTZ NOT NULL,
    pack_kwh_effective   DOUBLE PRECISION NOT NULL,
    soc_delta_pct        DOUBLE PRECISION NOT NULL,
    energy_delivered_kwh DOUBLE PRECISION NOT NULL,
    -- derate_active: true when chargerDerateStatus was "active" at
    -- any point in the session. Sample is still stored so we can
    -- explain dips in the chart, but flagged so trend lines can
    -- exclude it.
    derate_active        BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (vehicle_id, charge_id)
);

CREATE INDEX IF NOT EXISTS vehicle_pack_health_samples_vehicle_at
    ON vehicle_pack_health_samples (vehicle_id, at DESC);
