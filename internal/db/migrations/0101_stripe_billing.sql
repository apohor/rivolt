-- 0101_stripe_billing.sql: what the Stripe webhook needs to be idempotent and to map Stripe's objects to accounts. Card data never touches
-- Rivolt (hosted Checkout); these tables hold ids only.
--
--   stripe_events          every webhook event id seen: a retried or replayed delivery is acknowledged without being applied twice. No
--                          user column (an event id says nothing about a user), so no tenant policy.
--   billing_customers      the Stripe customer of an account (set when Checkout completes), and revoked_at: the event time of a full
--                          refund or a dispute, after which older subscription events are ignored so they cannot bring Pro back.
--   billing_stripe_subs    the newest event time applied per subscription, so events that arrive out of order cannot move an entitlement
--                          backwards.
CREATE TABLE IF NOT EXISTS stripe_events (
    event_id    TEXT        PRIMARY KEY CHECK (length(event_id) BETWEEN 1 AND 255),
    type        TEXT        NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS stripe_events_received ON stripe_events (received_at);

CREATE TABLE IF NOT EXISTS billing_customers (
    user_id            UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    stripe_customer_id TEXT        NOT NULL UNIQUE CHECK (length(stripe_customer_id) BETWEEN 1 AND 255),
    revoked_at         BIGINT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS billing_stripe_subs (
    subscription_id    TEXT        PRIMARY KEY CHECK (length(subscription_id) BETWEEN 1 AND 255),
    user_id            UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_event_created BIGINT      NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS billing_stripe_subs_user ON billing_stripe_subs (user_id);

DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['billing_customers', 'billing_stripe_subs'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
