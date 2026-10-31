-- The Monday readout, part 2: the numbers that need an OWNER-SIDE read, because the tables are not on the read-only role's allowlist
-- (internal/db/readonly.go): `events` (landing_view, plan_run, statement_share), `push_subscriptions` and `push_apns_devices`
-- (notification switches) and `engagement_prefs` / `statement_email_prefs` (email and recap switches). Run it with the lead's connection.
-- Counts only, nothing written. The events table holds a random per-browser id and allowlisted props (referrer host and utm_source are
-- tokens, never URLs); the queries below return neither ids nor props beyond those two tokens.
-- Variables as in monday_ro.sql: :from_ts, :to_ts.

\echo
\echo '== Landing views (landing_view), by referrer host and utm_source'
SELECT count(*) AS views_total,
       count(DISTINCT anon_id) AS distinct_browsers
  FROM events
 WHERE name = 'landing_view' AND ts >= :'from_ts'::timestamptz AND ts < :'to_ts'::timestamptz;

SELECT COALESCE(props->>'referrer_host', '(none)') AS referrer_host,
       COALESCE(props->>'utm_source', '(none)') AS utm_source,
       count(*) AS views
  FROM events
 WHERE name = 'landing_view' AND ts >= :'from_ts'::timestamptz AND ts < :'to_ts'::timestamptz
 GROUP BY 1, 2
 ORDER BY views DESC, 1, 2
 LIMIT 25;

\echo
\echo '== Planner runs (plan_run) and statement shares (statement_share)'
SELECT name, count(*) AS events, count(DISTINCT COALESCE(user_id::text, anon_id)) AS distinct_people
  FROM events
 WHERE name IN ('plan_run', 'statement_share') AND ts >= :'from_ts'::timestamptz AND ts < :'to_ts'::timestamptz
 GROUP BY name
 ORDER BY name;

SELECT props->>'kind' AS statement_share_kind, count(*) AS events
  FROM events
 WHERE name = 'statement_share' AND ts >= :'from_ts'::timestamptz AND ts < :'to_ts'::timestamptz
 GROUP BY 1
 ORDER BY 1;

\echo
\echo '== Accounts with any alert on'
-- any_switch_on: some notification switch is on for at least one of the account's devices. Several are on by default the moment a device
-- subscribes (charging done, anomaly, software update), so also see opted_in_extra: plug-in reminder, milestones, left-open, live charging.
WITH dev AS (
  SELECT user_id, 'web' AS kind,
         (on_charging_done OR on_plug_in_reminder OR on_anomaly OR on_software_update OR on_milestones OR on_left_open) AS any_on,
         (on_plug_in_reminder OR on_milestones OR on_left_open) AS extra_on
    FROM push_subscriptions
  UNION ALL
  SELECT user_id, 'iphone',
         (on_charging_done OR on_plug_in_reminder OR on_anomaly OR on_software_update OR on_milestones OR on_left_open OR on_charging_live),
         (on_plug_in_reminder OR on_milestones OR on_left_open OR on_charging_live)
    FROM push_apns_devices
), people AS (
  SELECT d.user_id, bool_or(d.any_on) AS any_on, bool_or(d.extra_on) AS extra_on, bool_or(d.kind = 'web' AND d.any_on) AS web_on,
         bool_or(d.kind = 'iphone' AND d.any_on) AS iphone_on
    FROM dev d JOIN users u ON u.id = d.user_id AND u.username <> 'rivolt-public-demo'
   GROUP BY d.user_id
)
SELECT count(*) FILTER (WHERE any_on) AS any_switch_on,
       count(*) FILTER (WHERE extra_on) AS opted_in_extra,
       count(*) FILTER (WHERE web_on) AS on_web,
       count(*) FILTER (WHERE iphone_on) AS on_iphone
  FROM people;

SELECT (SELECT count(*) FROM engagement_prefs p JOIN users u ON u.id = p.user_id AND u.username <> 'rivolt-public-demo' WHERE p.weekly_recap) AS weekly_recap_on,
       (SELECT count(*) FROM engagement_prefs p JOIN users u ON u.id = p.user_id AND u.username <> 'rivolt-public-demo' WHERE p.trip_recap) AS trip_recap_on,
       (SELECT count(*) FROM statement_email_prefs p JOIN users u ON u.id = p.user_id AND u.username <> 'rivolt-public-demo' WHERE p.enabled) AS statement_email_on,
       (SELECT count(*) FROM engagement_prefs p JOIN users u ON u.id = p.user_id AND u.username <> 'rivolt-public-demo' WHERE p.r2_arrival_month IS NOT NULL) AS r2_arrival_reminder_set;
