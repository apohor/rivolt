-- 0112_import_batches.sql: history import slice 1 (design: history-import-design.md section 4). Additive only: nothing reads or writes the new
-- columns yet. A nullable column without a default is a catalog change (no rewrite of drives or charges).
--   imports.undone_at   set when the user undoes the import; the rows it brought in are deleted in the same transaction
--   drives.import_id / charges.import_id   the batch a row came from (NULL for live rows and for every row that exists today); no backfill
-- The foreign keys are added NOT VALID (no scan under the lock) and validated in their own later migrations (0115, 0116), where the scan takes
-- only SHARE UPDATE EXCLUSIVE and does not block writes. ON DELETE SET NULL: deleting a batch row never deletes the user's rows.
-- SET LOCAL lock_timeout bounds the wait for the brief locks on drives, charges and imports; a busy table fails the migration, which retries.
-- Rollback: the code is reverted and the columns stay (nullable, unused).
SET LOCAL lock_timeout = '5s';

ALTER TABLE imports ADD COLUMN IF NOT EXISTS undone_at TIMESTAMPTZ;
ALTER TABLE drives  ADD COLUMN IF NOT EXISTS import_id UUID;
ALTER TABLE charges ADD COLUMN IF NOT EXISTS import_id UUID;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'drives_import_id_fkey') THEN
        ALTER TABLE drives ADD CONSTRAINT drives_import_id_fkey FOREIGN KEY (import_id) REFERENCES imports(id) ON DELETE SET NULL NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'charges_import_id_fkey') THEN
        ALTER TABLE charges ADD CONSTRAINT charges_import_id_fkey FOREIGN KEY (import_id) REFERENCES imports(id) ON DELETE SET NULL NOT VALID;
    END IF;
END $$;
