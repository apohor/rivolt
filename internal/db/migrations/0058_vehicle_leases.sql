-- 0058_vehicle_leases.sql — per-vehicle lease terms for the lease tracker.
-- One row per vehicle. Cascades with the vehicle and the user, so account
-- delete removes it; it is part of the data export.
CREATE TABLE IF NOT EXISTS vehicle_leases (
    vehicle_id          UUID PRIMARY KEY REFERENCES vehicles(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    start_date          DATE NOT NULL,
    end_date            DATE NOT NULL,
    start_odometer_mi   DOUBLE PRECISION NOT NULL,
    allowed_miles_total DOUBLE PRECISION NOT NULL,
    overage_per_mile    DOUBLE PRECISION NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (end_date > start_date),
    CHECK (allowed_miles_total > 0),
    CHECK (start_odometer_mi >= 0),
    CHECK (overage_per_mile >= 0)
);
CREATE INDEX IF NOT EXISTS vehicle_leases_user_id ON vehicle_leases (user_id);
