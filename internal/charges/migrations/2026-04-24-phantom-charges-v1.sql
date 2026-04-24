-- Delete pre-v0.3.54 phantom charge rows left behind by the live recorder,
-- which could open a session when charger_state briefly flickered into
-- "charging_*" while the car was unplugged. Those rows inherit the cached
-- Parallax frame (25.7 kWh, etc) and show StartSoC == EndSoC.
-- The v0.3.54 recorder gate (isPluggedCS && isChargingCS) prevents new
-- phantoms from being written; this one-shot cleans up existing ones.
DELETE FROM charges WHERE (end_soc_pct - start_soc_pct) < 1.0;
