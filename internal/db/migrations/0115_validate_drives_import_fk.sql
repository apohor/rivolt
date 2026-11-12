-- 0115_validate_drives_import_fk.sql: history import slice 1. Validates the NOT VALID foreign key from 0112 (a scan of drives under
-- SHARE UPDATE EXCLUSIVE: reads and writes continue). One table per migration to keep the lock window of each short.
SET LOCAL lock_timeout = '5s';
ALTER TABLE drives VALIDATE CONSTRAINT drives_import_id_fkey;
