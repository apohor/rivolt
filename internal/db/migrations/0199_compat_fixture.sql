SET LOCAL lock_timeout = '2s';
ALTER TABLE drives ADD COLUMN compat_fixture int NOT NULL;
