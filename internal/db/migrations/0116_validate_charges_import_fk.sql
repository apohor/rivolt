-- 0116_validate_charges_import_fk.sql: history import slice 1. See 0115.
SET LOCAL lock_timeout = '5s';
ALTER TABLE charges VALIDATE CONSTRAINT charges_import_id_fkey;
