-- 0103_stripe_account_closure.sql: account deletion cancels the account's Stripe subscriptions (Pro 8).
--
-- A deletion cancels at Stripe first; when Stripe cannot be reached the cancel is queued in idp_delete_failures (the same retry queue the
-- identity-provider and Sign in with Apple steps use) as kind 'stripe', keyed "stripe:<customer id>". The row holds the Stripe customer id and
-- an error text only: no user id (the account is gone), no email, no token. It is retried until it works and never expires on its own: a
-- deleted account must never be charged again, so an old row is an alert, not something to drop.
--
-- stripe_closed_customers remembers, for 30 days, the customers of accounts that were deleted. Stripe keeps sending events about a cancelled
-- subscription (customer.subscription.deleted, a last invoice), and with no account to map them to the webhook would answer 500 for three days
-- and Stripe would, in the end, switch the endpoint off. A customer in this table has its events acknowledged. Ids only.
DO $$
BEGIN
    ALTER TABLE idp_delete_failures DROP CONSTRAINT IF EXISTS idp_delete_failures_kind_check;
    ALTER TABLE idp_delete_failures ADD CONSTRAINT idp_delete_failures_kind_check CHECK (
        (kind = 'kratos' AND sealed IS NULL AND seal_uid IS NULL)
        OR (kind = 'apple' AND sealed IS NOT NULL AND seal_uid IS NOT NULL AND username = 'apple:' || seal_uid::text)
        OR (kind = 'stripe' AND sealed IS NULL AND seal_uid IS NULL AND username LIKE 'stripe:cus\_%')
    );
END $$;

CREATE TABLE IF NOT EXISTS stripe_closed_customers (
    customer_id TEXT        PRIMARY KEY CHECK (length(customer_id) BETWEEN 1 AND 255),
    closed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS stripe_closed_customers_closed ON stripe_closed_customers (closed_at);
