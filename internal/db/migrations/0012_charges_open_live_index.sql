-- 0012_charges_open_live_index.sql
--
-- Tighten charges_user_open_live to a whitelist of states that
-- actually mean "physically still in the middle of a session". The
-- old predicate (final_state LIKE 'charging\_%' AND NOT IN
-- ('charging_complete','charging_station_err')) treated brief
-- terminal frames like charging_user_stopped and
-- charging_station_stopped as still-open, so resumeOpenCharge would
-- reattach to the just-closed row and absorb every subsequent
-- physical session into one absorber row. See v0.17.7 incident.
--
-- The store-side queries (LatestOpenLive, CloseStaleOpenLive,
-- CloseStaleOpenLiveBefore, ListStaleOpenLive, RefreshOpenLive,
-- AbandonOpenLive) all use the same whitelist now.

DROP INDEX IF EXISTS charges_user_open_live;

CREATE INDEX IF NOT EXISTS charges_user_open_live ON charges (user_id, vehicle_id, final_state)
    WHERE source = 'live'
      AND final_state IN ('charging_active', 'charging_ready', 'charging_connecting', 'waiting_on_charger');
