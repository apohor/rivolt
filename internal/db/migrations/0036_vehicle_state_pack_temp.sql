-- 0036_vehicle_state_pack_temp.sql — high-voltage battery pack cell
-- temperatures, in °C.
--
-- Source: Rivian's Parallax channel (energy.high_voltage.battery_state
-- RVM), decoded from protobuf — see internal/rivian/parallax.go. The
-- legacy vehicleState GraphQL selection Rivolt's monitor primarily uses
-- does not carry pack temperature; Parallax does. This is the first
-- field recorded via the additive-Parallax model: keep vehicleState as
-- the primary source, layer Parallax topics on top for what it lacks.
--
-- Three columns because Rivian reports the pack as avg / max / min cell
-- temperature. The max-min spread is a genuine thermal-imbalance signal
-- (a wide spread during a DC fast charge or a cold soak is worth
-- surfacing), so we keep all three rather than collapsing to one.
--
-- Nullable by design: NULL on rows written before this migration, on
-- ElectraFi imports, and on live rows recorded before the vehicle has
-- pushed a battery_state frame (it only streams while awake).

ALTER TABLE vehicle_state
    ADD COLUMN IF NOT EXISTS pack_temp_avg_c DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS pack_temp_max_c DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS pack_temp_min_c DOUBLE PRECISION;
