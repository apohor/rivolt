package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/fence"
)

// The OTA details writes. The sync that makes them runs on the owner of the vehicle's group
// (single-owner, lead mode), and each write carries that lease when the context has a token
// (fence.Exec): a pod whose lease is gone is refused with fence.ErrFenced. Without a token they run
// as they always did.

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
	n, err := fence.Exec(ctx, d, `
		INSERT INTO ota_notices (user_id, rivian_vehicle_id, kind, version, baseline)
		SELECT $1::uuid, $2::text, v.kind, v.version, TRUE
		  FROM (VALUES ($3::text, $4::text), ($5::text, $6::text)) AS v(kind, version)
		 WHERE v.version <> ''
		   AND NOT EXISTS (SELECT 1 FROM ota_notices WHERE user_id = $1 AND rivian_vehicle_id = $2)
		   AND `+fence.OK+`
		ON CONFLICT DO NOTHING`,
		uid, rivianVehicleID, OTAInstalled, installed, OTAAvailable, available)
	if err != nil {
		return false, fmt.Errorf("seed ota notices: %w", err)
	}
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
	n, err := fence.Exec(ctx, d, `
		INSERT INTO ota_notices (user_id, rivian_vehicle_id, kind, version)
		SELECT $1::uuid, $2::text, $3::text, $4::text
		 WHERE `+fence.OK+`
		ON CONFLICT DO NOTHING`, uid, rivianVehicleID, kind, version)
	if err != nil {
		return false, fmt.Errorf("claim ota notice: %w", err)
	}
	return n > 0, nil
}

// MarkOTAUnavailable records that Rivian said OTA details are not available
// for the vehicle, which keeps ClaimOTAFetch from winning for a week.
func MarkOTAUnavailable(ctx context.Context, d *sql.DB, userID uuid.UUID, rivianVehicleID string) error {
	_, err := fence.Exec(ctx, d, `
		UPDATE vehicles
		   SET metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{ota_unavailable_at}', to_jsonb(NOW()::text))
		 WHERE user_id = $1 AND rivian_vehicle_id = $2 AND `+fence.OK, userID, rivianVehicleID)
	return err
}

// ClaimOTAFetch records an attempt to fetch the vehicle's OTA details and
// reports whether this caller won it: no attempt in the last minInterval,
// across pods and restarts. It matches only the user's own vehicle.
func ClaimOTAFetch(ctx context.Context, d *sql.DB, userID uuid.UUID, rivianVehicleID string, minInterval time.Duration) (bool, error) {
	n, err := fence.Exec(ctx, d, `
		UPDATE vehicles
		   SET metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{ota_attempt_at}', to_jsonb(NOW()::text))
		 WHERE user_id = $1 AND rivian_vehicle_id = $2
		   AND ( metadata->>'ota_attempt_at' IS NULL
		      OR (metadata->>'ota_attempt_at')::timestamptz < NOW() - $3::interval )
		   AND ( metadata->>'ota_unavailable_at' IS NULL
		      OR (metadata->>'ota_unavailable_at')::timestamptz < NOW() - $4::interval )
		   AND `+fence.OK,
		userID, rivianVehicleID, fmt.Sprintf("%d microseconds", minInterval.Microseconds()),
		fmt.Sprintf("%d microseconds", OTAUnavailableBackoff.Microseconds()))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SetVehicleOTA stores the OTA details (a JSON object) for the user's
// vehicle, keeping every other metadata key.
func SetVehicleOTA(ctx context.Context, d *sql.DB, userID uuid.UUID, rivianVehicleID string, ota json.RawMessage) error {
	n, err := fence.Exec(ctx, d, `
		UPDATE vehicles
		   SET metadata = (COALESCE(metadata, '{}'::jsonb) - 'ota_unavailable_at') || jsonb_build_object('ota', $3::jsonb), updated_at = NOW()
		 WHERE user_id = $1 AND rivian_vehicle_id = $2 AND `+fence.OK, userID, rivianVehicleID, string(ota))
	if err != nil {
		return fmt.Errorf("vehicle ota update: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
