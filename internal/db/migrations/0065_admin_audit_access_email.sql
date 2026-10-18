-- 0065_admin_audit_access_email.sql — which Cloudflare Access identity was
-- behind an admin action. On the admin deployment every request passes
-- Cloudflare Access first; the VERIFIED email from its signed JWT (never the
-- plain Cf-Access-Authenticated-User-Email header) is recorded here next to
-- the Rivolt admin account. Empty on the public site, which has no Access.
ALTER TABLE admin_audit ADD COLUMN IF NOT EXISTS access_email TEXT NOT NULL DEFAULT '';
