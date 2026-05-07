-- invite_codes: single-use tokens an admin generates and shares with a
-- prospective beta user. The user redeems the code at /signup; the row
-- is never deleted so the admin panel can show usage history.
CREATE TABLE IF NOT EXISTS invite_codes (
    code        TEXT        PRIMARY KEY,
    created_by  UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    used_by     UUID        REFERENCES users(id) ON DELETE SET NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS invite_codes_created_by ON invite_codes (created_by);

-- onboarding_completed: false for brand-new accounts so the app can
-- show the first-run stepper. Existing users are pre-marked complete
-- so they never see the stepper on the next deploy.
ALTER TABLE users ADD COLUMN IF NOT EXISTS
    onboarding_completed BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE users SET onboarding_completed = TRUE
WHERE onboarding_completed = FALSE;
