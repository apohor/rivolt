-- 0010_charges_thermal_kwh.sql
-- Adds Parallax `thermal_kwh` to the charges table — energy the BMS
-- spent on pack heating / cooling during the session, decoded from
-- the ChargingSessionLiveData protobuf (field 3). The closest signal
-- Rivian's cloud APIs expose to a "battery temperature" reading; a
-- high value during a session means the pack was being aggressively
-- thermally managed (cold-soak DC fast-charge, hot ambient L2, etc).
--
-- Nullable + no default: legacy rows recorded before v0.10.17 stay
-- NULL so the UI can distinguish "unknown" from "zero".
ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS thermal_kwh DOUBLE PRECISION;
