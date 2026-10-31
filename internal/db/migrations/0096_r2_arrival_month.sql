-- 0096_r2_arrival_month.sql — "Tell me when my R2 may be here": the opt-in for the one arrival-month email.
--
--   engagement_prefs.r2_arrival_month  the month (a month, never a day: always the first of it) the user
--                                      expects an R2 delivery. NULL = no reminder, which is the state for
--                                      everyone until they choose a month. The job (internal/r2arrival) sends
--                                      one email on the first weekday of that month and never another for it.
--                                      The user changes or deletes the month in Settings; deleting the account
--                                      deletes the row (ON DELETE CASCADE on engagement_prefs).
--
-- Metadata-only ADD COLUMN, no rewrite.
ALTER TABLE engagement_prefs ADD COLUMN IF NOT EXISTS r2_arrival_month DATE
    CHECK (r2_arrival_month IS NULL OR r2_arrival_month = date_trunc('month', r2_arrival_month)::date);
