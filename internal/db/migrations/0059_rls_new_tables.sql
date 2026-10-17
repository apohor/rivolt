-- 0059_rls_new_tables.sql — tenant row-level-security policies (the 0008
-- pattern) for the per-user tables added since: rivian_receipts,
-- rivian_receipt_sync, ota_notices and vehicle_leases. Like 0008 this is
-- stage 1: ENABLE only, not FORCE, so the app role (the table owner) is
-- unaffected today and app-level user_id predicates stay the filter; the
-- policies are ready for the role split.
DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['rivian_receipts', 'rivian_receipt_sync', 'ota_notices', 'vehicle_leases'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
