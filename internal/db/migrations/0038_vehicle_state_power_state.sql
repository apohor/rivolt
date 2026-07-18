-- 0038_vehicle_state_power_state.sql — the vehicle's power/sleep state.
--
-- Source: the monitor's cached State.PowerState — vehicleState's
-- powerState ("sleep" | "ready" | "standby" | "go"), with Parallax
-- vehicle.power.state (3=ready / 4=go) layered on top where available.
-- Until now this only lived in the live cache and API; it was never
-- persisted, so there was no way to graph when the car was asleep vs
-- awake over time. This column makes that history durable.
--
-- Nullable by design: NULL on every row written before this migration
-- and on ElectraFi imports. The sleep/activity aggregation filters on
-- power_state IS NOT NULL, so it simply starts from the first row
-- recorded after the deploy and fills forward.

ALTER TABLE vehicle_state
    ADD COLUMN IF NOT EXISTS power_state TEXT;
