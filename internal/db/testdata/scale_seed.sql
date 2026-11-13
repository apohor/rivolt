-- Scale seed for the migration probe (migrate_probe_integration_test.go; migrations v2, step 4). Hand-written, not a dump of anything real.
-- Loaded on the BASE schema, then loaded again (a second __TAG__) after the new migration files, which proves the new schema still takes the
-- old binary's insert shape. Only columns that exist since 0001 are named, so an added column (or a dropped one: a failure) shows up here.
-- Fixed design scale: 100000 rows in each growable table, vehicle_state spread over three monthly partitions. Not a prod count.
-- __TAG__ is replaced by the harness (1, then 2); ids and keys derive from it so the rerun inserts new rows.

INSERT INTO users (id, username) VALUES ('00000000-0000-4000-8000-00000000a001', 'probe-seed') ON CONFLICT DO NOTHING;
INSERT INTO vehicles (id, user_id, rivian_vehicle_id)
VALUES ('00000000-0000-4000-8000-00000000b001', '00000000-0000-4000-8000-00000000a001', 'probe-seed-vehicle') ON CONFLICT DO NOTHING;

SELECT rivolt_ensure_vehicle_state_partition(t) FROM unnest(ARRAY['2026-01-15', '2026-02-15', '2026-03-15']::timestamptz[]) AS t;

-- 100000 samples 77 s apart from 2026-01-01 fall in January, February and March.
INSERT INTO vehicle_state (vehicle_id, at, user_id, battery_level_pct, range_mi, odometer_mi, lat, lon, speed_mph, shift_state, source)
SELECT '00000000-0000-4000-8000-00000000b001',
       TIMESTAMPTZ '2026-01-01 00:00:00+00' + g * INTERVAL '77 seconds' + __TAG__ * INTERVAL '1 second',
       '00000000-0000-4000-8000-00000000a001',
       20 + (g % 70), 100 + (g % 200), 5000 + g * 0.1, 34.0 + (g % 1000) * 0.0001, -118.0 - (g % 1000) * 0.0001, g % 75, 'P', 'probe'
FROM generate_series(1, 100000) AS g;

INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, start_soc_pct, end_soc_pct, distance_mi, source)
SELECT '00000000-0000-4000-8000-00000000a001', '00000000-0000-4000-8000-00000000b001', 'seed-__TAG__-' || g,
       TIMESTAMPTZ '2025-01-01 00:00:00+00' + g * INTERVAL '20 minutes', TIMESTAMPTZ '2025-01-01 00:10:00+00' + g * INTERVAL '20 minutes',
       80, 70, 5 + (g % 30), 'probe'
FROM generate_series(1, 100000) AS g;

INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, start_soc_pct, end_soc_pct, energy_added_kwh, source)
SELECT '00000000-0000-4000-8000-00000000a001', '00000000-0000-4000-8000-00000000b001', 'seed-__TAG__-' || g,
       TIMESTAMPTZ '2025-01-01 00:00:00+00' + g * INTERVAL '20 minutes', TIMESTAMPTZ '2025-01-01 00:10:00+00' + g * INTERVAL '20 minutes',
       30, 60, 10 + (g % 40), 'probe'
FROM generate_series(1, 100000) AS g;

ANALYZE vehicle_state;
ANALYZE drives;
ANALYZE charges;
