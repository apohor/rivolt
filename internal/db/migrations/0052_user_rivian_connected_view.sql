-- user_rivian_connected: which users have a stored Rivian session, as a
-- boolean only, for the read-only reporting role (grafana_ro). It can't
-- read user_secrets (encrypted sessions), so a Grafana alert on "a
-- connected vehicle recorded nothing for 2 h" could only see leased
-- vehicles; a vehicle no pod leases slipped through. A view runs with
-- its owner's privileges, so the reporting role reads the boolean
-- without any grant on user_secrets. Granted via readonlyTables
-- (internal/db/readonly.go).
CREATE OR REPLACE VIEW user_rivian_connected AS
SELECT u.id AS user_id,
       EXISTS (SELECT 1 FROM user_secrets s
               WHERE s.user_id = u.id AND s.name = 'rivian.session') AS has_rivian_session
FROM users u;
