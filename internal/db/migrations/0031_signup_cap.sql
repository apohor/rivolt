-- 0031_signup_cap.sql — operator-controlled OAuth signup cap.
--
-- Stored as a flags row (see 0003_flags.sql) so the existing 10s
-- poll + admin-write plumbing covers it without a parallel table.
-- value JSON: {"limit": 100}.
--
-- The cap is enforced in the OIDC callback before users.insert
-- (and before any IdP provisioning). Existing users signing back
-- in are exempt — the cap only gates new-account creation.

INSERT INTO flags (name, value, updated_by)
VALUES ('signup_cap', '{"limit": 100}'::jsonb, 'migration:0031')
ON CONFLICT (name) DO NOTHING;
