-- vehicle_state.charger_status: Rivian's chargerStatus (the plug
-- indicator: chrgr_sts_connected_*, chrgr_sts_not_connected), stored
-- next to charging_state. charging_state alone says "plugged" wrongly
-- for an R2, which reports charging_ready while driving and parked
-- unplugged; /now and analyses can require a connected status instead.
-- NULL on rows written before this column (and when not reported).
ALTER TABLE vehicle_state ADD COLUMN IF NOT EXISTS charger_status text;
