-- 0070_partition_guard.sql — the vehicle_state partition helper refuses
-- unreasonable months and cannot wait forever on a lock.
--
-- rivolt_ensure_vehicle_state_partition(ts) created a partition for ANY
-- timestamp, so a sample dated in the year 9999 (or a file of scattered dates)
-- created a partition per month, each CREATE TABLE ... PARTITION OF taking a lock
-- on vehicle_state that blocks the live recorder behind it. The Go writers now
-- refuse samples outside 2018-01-01 .. now+1 day; this is the same bound in the
-- database (a month before 2018, or more than 13 months ahead, is an error), plus
-- a lock_timeout of 5 s for the helper's own statements, restored afterwards.
--
-- Rollback: CREATE OR REPLACE the function with the 0007 definition (no
-- bounds, no lock_timeout). Existing partitions are untouched.
CREATE OR REPLACE FUNCTION rivolt_ensure_vehicle_state_partition(when_ts TIMESTAMPTZ)
RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    month_start TIMESTAMPTZ := date_trunc('month', when_ts);
    month_end   TIMESTAMPTZ := month_start + INTERVAL '1 month';
    part_name   TEXT        := 'vehicle_state_y' || to_char(month_start AT TIME ZONE 'UTC', 'YYYY_MM');
    old_timeout TEXT;
BEGIN
    IF month_start < TIMESTAMPTZ '2018-01-01 00:00:00+00'
       OR month_start > date_trunc('month', now()) + INTERVAL '13 months' THEN
        RAISE EXCEPTION 'vehicle_state partition for % is outside the accepted range', to_char(month_start AT TIME ZONE 'UTC', 'YYYY-MM');
    END IF;
    IF to_regclass(part_name) IS NULL THEN
        old_timeout := current_setting('lock_timeout');
        PERFORM set_config('lock_timeout', '5s', true);
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF vehicle_state FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        PERFORM set_config('lock_timeout', old_timeout, true);
    END IF;
    RETURN part_name;
END;
$$;
