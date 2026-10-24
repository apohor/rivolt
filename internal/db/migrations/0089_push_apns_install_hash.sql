-- 0089_push_apns_install_hash.sql: who may move an APNs device row to another account.
--
-- A device token is one iPhone install, and a registration of it by another account moves the row (right when one phone
-- switches accounts after a sign-out that never reached the server). The app now sends a random install id it keeps in its
-- Keychain; the server keeps only sha256 of it here. A row with a hash under a live session moves to another account only for
-- the same install id, so someone who learns a token cannot take the registration. Rows registered before this (NULL) move as
-- before until their app registers again. The hash is never logged, exported or backed up, and goes with the row.
--
-- Rollback: ALTER TABLE push_apns_devices DROP COLUMN install_hash;

ALTER TABLE push_apns_devices ADD COLUMN IF NOT EXISTS install_hash TEXT;
