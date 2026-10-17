-- 0061_terms_acceptance.sql — which version of the Terms each user accepted,
-- and when. NULL = never accepted. Metadata-only ADD COLUMNs. The version is
-- the server's date-stamped constant; the client gates the UI, nothing is
-- blocked server-side.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS terms_accepted_version TEXT,
    ADD COLUMN IF NOT EXISTS terms_accepted_at TIMESTAMPTZ;
