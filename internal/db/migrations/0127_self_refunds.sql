-- 0125_self_refunds.sql
--
-- Self-serve refunds of web Pro (Settings, Account, Plan, "Refund and cancel"): within 14 days of a payment, one tap refunds that payment in
-- full through the Stripe API and ends Pro at once. One row per account is the whole limit: the primary key on user_id is "one self-serve
-- refund per account", and it is also the record of what was refunded.
--
--   state     'pending' from the moment the refund is reserved until Stripe has accepted it, 'done' after. A retry of a pending row sends the
--             same Stripe Idempotency-Key, so a double tap or a replay can never refund twice. A refund Stripe refuses outright removes the
--             pending row, so a failed attempt does not use up the account's one refund.
--   charge_id the Stripe charge refunded (an id, no card data); refund_id the Stripe refund once accepted.
SET LOCAL lock_timeout = '2s';

CREATE TABLE IF NOT EXISTS billing_self_refunds (
    user_id      UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    charge_id    TEXT        NOT NULL UNIQUE CHECK (length(charge_id) BETWEEN 1 AND 255),
    refund_id    TEXT        CHECK (refund_id IS NULL OR length(refund_id) BETWEEN 1 AND 255),
    amount_cents BIGINT      NOT NULL CHECK (amount_cents >= 0),
    currency     TEXT        NOT NULL CHECK (length(currency) = 3),
    state        TEXT        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    done_at      TIMESTAMPTZ
);

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['billing_self_refunds'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
