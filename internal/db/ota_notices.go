package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// OTA notice kinds.
const (
	OTAAvailable = "available"
	OTAInstalled = "installed"
)

// SeedOTANotices records a vehicle's first-seen versions as the silent
// baseline, and reports whether it did. It does nothing (false) once the
// vehicle has any notice, so only a vehicle's very first sync is seeded and
// nobody is alerted about versions that predate the feature. An empty
// version is skipped.
func SeedOTANotices(ctx context.Context, d *sql.DB, uid uuid.UUID, rivianVehicleID, installed, available string) (bool, error) {
	res, err := d.ExecContext(ctx, `
		INSERT INTO ota_notices (user_id, rivian_vehicle_id, kind, version, baseline)
		SELECT $1, $2, v.kind, v.version, TRUE
		  FROM (VALUES ($3, $4), ($5, $6)) AS v(kind, version)
		 WHERE v.version <> ''
		   AND NOT EXISTS (SELECT 1 FROM ota_notices WHERE user_id = $1 AND rivian_vehicle_id = $2)
		ON CONFLICT DO NOTHING`,
		uid, rivianVehicleID, OTAInstalled, installed, OTAAvailable, available)
	if err != nil {
		return false, fmt.Errorf("seed ota notices: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClaimOTANotice records that the (vehicle, kind, version) alert is being
// sent and reports whether this caller won it: false means it was already
// announced, so nothing is sent again (restarts, a second pod, a version
// that flaps back).
func ClaimOTANotice(ctx context.Context, d *sql.DB, uid uuid.UUID, rivianVehicleID, kind, version string) (bool, error) {
	if version == "" {
		return false, nil
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO ota_notices (user_id, rivian_vehicle_id, kind, version)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, uid, rivianVehicleID, kind, version)
	if err != nil {
		return false, fmt.Errorf("claim ota notice: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
