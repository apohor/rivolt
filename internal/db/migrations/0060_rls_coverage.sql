-- 0060_rls_coverage.sql — tenant row-level security on every remaining
-- per-user table (the 0008 pattern; stage 1: ENABLE, not FORCE, so the
-- table-owner app role is unaffected and app-level user_id predicates stay
-- the filter, the policies are ready for the role split).
--
-- Covers tables added after 0008 that carry a user_id: the newer receipts,
-- OTA-notice and lease tables (idempotent over 0059), plus charge_restamps,
-- charges_deleted_backup, drive_efficiency, drive_recaps, drive_weather,
-- drive_weather_series, events, saved_trips, statement_email_prefs,
-- statement_email_sends and trip_plan_links.
--
-- vehicle_pack_health_samples has no user_id, only a vehicle_id, so its policy
-- goes through the vehicle's owner.
--
-- Deliberately without a policy (no tenant): migrations, flags, app_settings,
-- push_vapid, signup_requests (pre-account waitlist, admin only),
-- idp_delete_failures (system retry queue) and subscription_leases (pod
-- bookkeeping keyed by Rivian vehicle id).
--
-- events.user_id is nullable (system events): under the policy a tenant sees
-- only its own rows; system rows stay visible to the owner role only.
DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'rivian_receipts', 'rivian_receipt_sync', 'ota_notices', 'vehicle_leases',
        'charge_restamps', 'charges_deleted_backup', 'drive_efficiency', 'drive_recaps',
        'drive_weather', 'drive_weather_series', 'events', 'saved_trips',
        'statement_email_prefs', 'statement_email_sends', 'trip_plan_links'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;

ALTER TABLE vehicle_pack_health_samples ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS vehicle_pack_health_samples_tenant_isolation ON vehicle_pack_health_samples;
CREATE POLICY vehicle_pack_health_samples_tenant_isolation ON vehicle_pack_health_samples
    USING (EXISTS (SELECT 1 FROM vehicles v WHERE v.id = vehicle_id AND v.user_id = rivolt_current_user_id()))
    WITH CHECK (EXISTS (SELECT 1 FROM vehicles v WHERE v.id = vehicle_id AND v.user_id = rivolt_current_user_id()));
