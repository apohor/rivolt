-- The Monday readout, part 1: everything the read-only reporting role (grafana_ro) can read.
-- Counts only: no ids, emails, names, IPs, places or amounts come out of these queries, and nothing is written.
-- Variables (set by run.sh; psql -v): :from_ts and :to_ts, the half-open week [from_ts, to_ts) in UTC.
-- The demo account (username rivolt-public-demo) is left out of every number.

\echo
\echo '== Window'
SELECT :'from_ts'::timestamptz AS week_from_utc, :'to_ts'::timestamptz AS week_to_utc;

\echo
\echo '== Sign-ups (accounts created)'
SELECT count(*) FILTER (WHERE created_at >= :'from_ts'::timestamptz AND created_at < :'to_ts'::timestamptz) AS this_week,
       count(*) FILTER (WHERE created_at >= :'from_ts'::timestamptz - interval '7 days' AND created_at < :'from_ts'::timestamptz) AS previous_week,
       count(*) FILTER (WHERE created_at < :'to_ts'::timestamptz) AS accounts_total
  FROM users
 WHERE username <> 'rivolt-public-demo';

\echo
\echo '== Connected (a Rivian account linked)'
-- first_vehicle: accounts whose first live vehicle row appeared this week (imported electrafi_ vehicles do not count).
-- connected_now: accounts holding a Rivian session right now (user_rivian_connected is a boolean view; never the session).
-- cohort: of this week's sign-ups, how many have connected by now.
WITH first_vehicle AS (
  SELECT v.user_id, min(v.created_at) AS at
    FROM vehicles v
    JOIN users u ON u.id = v.user_id AND u.username <> 'rivolt-public-demo'
   WHERE v.rivian_vehicle_id NOT LIKE 'electrafi%'
   GROUP BY v.user_id
)
SELECT (SELECT count(*) FROM first_vehicle WHERE at >= :'from_ts'::timestamptz AND at < :'to_ts'::timestamptz) AS first_vehicle_this_week,
       (SELECT count(*) FROM first_vehicle WHERE at >= :'from_ts'::timestamptz - interval '7 days' AND at < :'from_ts'::timestamptz) AS first_vehicle_previous_week,
       (SELECT count(*) FROM user_rivian_connected c JOIN users u ON u.id = c.user_id
         WHERE c.has_rivian_session AND u.username <> 'rivolt-public-demo') AS connected_now,
       (SELECT count(*) FROM users u JOIN first_vehicle f ON f.user_id = u.id
         WHERE u.created_at >= :'from_ts'::timestamptz AND u.created_at < :'to_ts'::timestamptz) AS this_weeks_signups_connected_by_now;

\echo
\echo '== First data (the first drive, charge or recorded state)'
-- Per account the earliest of: first drive, first charge, first vehicle_state row (one index probe per vehicle).
WITH first_data AS (
  SELECT user_id, min(at) AS at FROM (
    SELECT user_id, min(started_at) AS at FROM drives GROUP BY user_id
    UNION ALL
    SELECT user_id, min(started_at) FROM charges GROUP BY user_id
    UNION ALL
    SELECT v.user_id, (SELECT s.at FROM vehicle_state s WHERE s.vehicle_id = v.id ORDER BY s.at LIMIT 1)
      FROM vehicles v WHERE v.rivian_vehicle_id NOT LIKE 'electrafi%'
  ) x WHERE at IS NOT NULL GROUP BY user_id
), real AS (
  SELECT f.user_id, f.at, u.created_at FROM first_data f
    JOIN users u ON u.id = f.user_id AND u.username <> 'rivolt-public-demo'
)
SELECT count(*) FILTER (WHERE at >= :'from_ts'::timestamptz AND at < :'to_ts'::timestamptz) AS first_data_this_week,
       count(*) FILTER (WHERE at >= :'from_ts'::timestamptz - interval '7 days' AND at < :'from_ts'::timestamptz) AS first_data_previous_week,
       count(*) FILTER (WHERE created_at >= :'from_ts'::timestamptz AND created_at < :'to_ts'::timestamptz) AS this_weeks_signups_with_data_by_now
  FROM real;

\echo
\echo '== Weekly active (a drive or a charge started in the week)'
WITH act AS (
  SELECT user_id, started_at AS at FROM drives
  UNION ALL
  SELECT user_id, started_at FROM charges
)
SELECT count(DISTINCT a.user_id) FILTER (WHERE a.at >= :'from_ts'::timestamptz AND a.at < :'to_ts'::timestamptz) AS active_this_week,
       count(DISTINCT a.user_id) FILTER (WHERE a.at >= :'from_ts'::timestamptz - interval '7 days' AND a.at < :'from_ts'::timestamptz) AS active_previous_week,
       count(DISTINCT a.user_id) FILTER (WHERE a.at >= :'from_ts'::timestamptz - interval '7 days' AND a.at < :'from_ts'::timestamptz
         AND (EXISTS (SELECT 1 FROM drives d WHERE d.user_id = a.user_id AND d.started_at >= :'from_ts'::timestamptz AND d.started_at < :'to_ts'::timestamptz)
              OR EXISTS (SELECT 1 FROM charges c WHERE c.user_id = a.user_id AND c.started_at >= :'from_ts'::timestamptz AND c.started_at < :'to_ts'::timestamptz))) AS active_both_weeks
  FROM act a
  JOIN users u ON u.id = a.user_id AND u.username <> 'rivolt-public-demo';
