package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Per-user rows that are in the data export and the backup beside drives,
// charges and samples: saved trips (chosen places), the generated recap and
// efficiency analysis of a drive, and milestones. Each is read and written with
// plain user-scoped SQL; the writes are upserts on the table's own key, so a
// restore can run again and every row lands once.

type savedTripRow struct {
	Name      string          `json:"name"`
	Inputs    json.RawMessage `json:"inputs"`
	Plan      json.RawMessage `json:"plan,omitempty"`
	Advice    json.RawMessage `json:"advice,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type driveRecapRow struct {
	DriveID      string    `json:"drive_id"` // the drive's external id
	Model        string    `json:"model"`
	Recap        string    `json:"recap"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	GeneratedAt  time.Time `json:"generated_at"`
}

type driveAnalysisRow struct {
	DriveID      string          `json:"drive_id"`
	Model        string          `json:"model"`
	AnalysisText string          `json:"analysis_text"`
	ResultJSON   json.RawMessage `json:"result_json"`
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
	GeneratedAt  time.Time       `json:"generated_at"`
}

type milestoneRow struct {
	RivianVehicleID string          `json:"rivian_vehicle_id"`
	Key             string          `json:"key"`
	Status          string          `json:"status"`
	AchievedAt      time.Time       `json:"achieved_at"`
	Detail          json.RawMessage `json:"detail"`
	DismissedAt     *time.Time      `json:"dismissed_at,omitempty"`
}

func nullJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}

func eachSavedTrip(ctx context.Context, d *sql.DB, uid uuid.UUID, fn func(savedTripRow) error) error {
	rows, err := d.QueryContext(ctx, `SELECT name, inputs, plan, advice, created_at, updated_at FROM saved_trips WHERE user_id = $1 ORDER BY name`, uid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r savedTripRow
		var in, plan, adv []byte
		if err := rows.Scan(&r.Name, &in, &plan, &adv, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return err
		}
		r.Inputs, r.Plan, r.Advice = nullJSON(in), nullJSON(plan), nullJSON(adv)
		r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

func eachDriveRecap(ctx context.Context, d *sql.DB, uid uuid.UUID, fn func(driveRecapRow) error) error {
	rows, err := d.QueryContext(ctx, `SELECT drive_id, model, recap, input_tokens, output_tokens, generated_at FROM drive_recaps WHERE user_id = $1 ORDER BY drive_id`, uid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r driveRecapRow
		if err := rows.Scan(&r.DriveID, &r.Model, &r.Recap, &r.InputTokens, &r.OutputTokens, &r.GeneratedAt); err != nil {
			return err
		}
		r.GeneratedAt = r.GeneratedAt.UTC()
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

func eachDriveAnalysis(ctx context.Context, d *sql.DB, uid uuid.UUID, fn func(driveAnalysisRow) error) error {
	rows, err := d.QueryContext(ctx, `SELECT drive_id, model, analysis_text, result_json, input_tokens, output_tokens, generated_at FROM drive_efficiency WHERE user_id = $1 ORDER BY drive_id`, uid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r driveAnalysisRow
		var res []byte
		if err := rows.Scan(&r.DriveID, &r.Model, &r.AnalysisText, &res, &r.InputTokens, &r.OutputTokens, &r.GeneratedAt); err != nil {
			return err
		}
		r.ResultJSON = nullJSON(res)
		r.GeneratedAt = r.GeneratedAt.UTC()
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

func eachMilestone(ctx context.Context, d *sql.DB, uid uuid.UUID, fn func(milestoneRow) error) error {
	rows, err := d.QueryContext(ctx, `
		SELECT v.rivian_vehicle_id, m.key, m.status, m.achieved_at, m.detail, m.dismissed_at
		  FROM milestones m JOIN vehicles v ON v.id = m.vehicle_id
		 WHERE m.user_id = $1 ORDER BY v.rivian_vehicle_id, m.key`, uid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r milestoneRow
		var detail []byte
		var dismissed sql.NullTime
		if err := rows.Scan(&r.RivianVehicleID, &r.Key, &r.Status, &r.AchievedAt, &detail, &dismissed); err != nil {
			return err
		}
		r.Detail = nullJSON(detail)
		r.AchievedAt = r.AchievedAt.UTC()
		if dismissed.Valid {
			t := dismissed.Time.UTC()
			r.DismissedAt = &t
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

func jsonOrDefault(b json.RawMessage, def string) []byte {
	if len(b) == 0 || string(b) == "null" {
		return []byte(def)
	}
	return b
}

// The upserts below are the restore side. Each is keyed on the table's own key.

func upsertSavedTrip(ctx context.Context, d *sql.DB, uid uuid.UUID, r savedTripRow) error {
	plan, adv := []byte(nil), []byte(nil)
	if len(r.Plan) > 0 && string(r.Plan) != "null" {
		plan = sanitizeRestoredJSON(r.Plan)
	}
	if len(r.Advice) > 0 && string(r.Advice) != "null" {
		adv = sanitizeRestoredJSON(r.Advice)
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO saved_trips (user_id, name, inputs, plan, advice, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (user_id, name) DO UPDATE SET inputs = EXCLUDED.inputs, plan = EXCLUDED.plan,
		       advice = EXCLUDED.advice, created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at`,
		uid, r.Name, jsonOrDefault(r.Inputs, "{}"), plan, adv, r.CreatedAt, r.UpdatedAt)
	return err
}

func upsertDriveRecap(ctx context.Context, d *sql.DB, uid uuid.UUID, r driveRecapRow) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO drive_recaps (user_id, drive_id, model, recap, input_tokens, output_tokens, generated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (user_id, drive_id) DO UPDATE SET model = EXCLUDED.model, recap = EXCLUDED.recap,
		       input_tokens = EXCLUDED.input_tokens, output_tokens = EXCLUDED.output_tokens, generated_at = EXCLUDED.generated_at`,
		uid, r.DriveID, r.Model, r.Recap, r.InputTokens, r.OutputTokens, r.GeneratedAt)
	return err
}

func upsertDriveAnalysis(ctx context.Context, d *sql.DB, uid uuid.UUID, r driveAnalysisRow) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO drive_efficiency (user_id, drive_id, model, analysis_text, result_json, input_tokens, output_tokens, generated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (user_id, drive_id) DO UPDATE SET model = EXCLUDED.model, analysis_text = EXCLUDED.analysis_text,
		       result_json = EXCLUDED.result_json, input_tokens = EXCLUDED.input_tokens,
		       output_tokens = EXCLUDED.output_tokens, generated_at = EXCLUDED.generated_at`,
		uid, r.DriveID, r.Model, r.AnalysisText, jsonOrDefault(r.ResultJSON, "{}"), r.InputTokens, r.OutputTokens, r.GeneratedAt)
	return err
}

// upsertMilestone restores a milestone for a vehicle the user owns; the vehicle
// is matched by its Rivian id.
func upsertMilestone(ctx context.Context, d *sql.DB, uid uuid.UUID, r milestoneRow) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO milestones (user_id, vehicle_id, key, status, achieved_at, detail, dismissed_at)
		SELECT $1, v.id, $3, $4, $5, $6, $7 FROM vehicles v WHERE v.user_id = $1 AND v.rivian_vehicle_id = $2
		ON CONFLICT (user_id, vehicle_id, key) DO UPDATE SET status = EXCLUDED.status,
		       achieved_at = EXCLUDED.achieved_at, detail = EXCLUDED.detail, dismissed_at = EXCLUDED.dismissed_at`,
		uid, r.RivianVehicleID, r.Key, r.Status, r.AchievedAt, jsonOrDefault(r.Detail, "{}"), r.DismissedAt)
	return err
}

// eachDerivedRow streams a per-user table whose rows are derived from a drive
// (stored weather), as JSON objects with the user_id left out. Table is one of
// derivedTables, never user input. Used for the export only: these are regenerable,
// so the backup leaves them out and says so (derived_omitted).
func eachDerivedRow(ctx context.Context, d *sql.DB, uid uuid.UUID, table string, fn func(json.RawMessage) error) error {
	order := map[string]string{"drive_weather": "drive_id", "drive_weather_series": "drive_id, sampled_at"}[table]
	if order == "" {
		return fmt.Errorf("not a derived table: %q", table)
	}
	rows, err := d.QueryContext(ctx, `SELECT to_jsonb(t) - 'user_id' FROM `+table+` t WHERE user_id = $1 ORDER BY `+order, uid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return err
		}
		if err := fn(json.RawMessage(b)); err != nil {
			return err
		}
	}
	return rows.Err()
}

// derivedOmittedFromBackup lists what the backup does not carry because it can
// be regenerated; the backup file names it.
var derivedOmittedFromBackup = []string{"drive_weather", "drive_weather_series"}

// allowedRestoredURLPrefixes are the only link targets a restored plan or advice may
// carry: the third-party price source the planner itself emits (dcfcrates.ObservedFor).
// A plan comes from a file the user uploads, and the UI renders its source_url as a link
// (also on the admin origin while viewing as the user), so a URL the server did not
// produce is dropped.
var allowedRestoredURLPrefixes = []string{"https://dcfctracker.com/"}

// sanitizeRestoredJSON removes every URL-like field (a key that is, or ends in, url,
// href or link, in any case) from a restored saved-trip plan or advice, at any depth,
// unless its value is a string starting with an allowed prefix. Other content is kept
// as it was; a document that is not valid JSON is returned unchanged for the database
// to reject.
func sanitizeRestoredJSON(raw json.RawMessage) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers round-trip exactly
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(stripURLFields(v))
	if err != nil {
		return raw
	}
	return out
}

func isURLKey(k string) bool {
	k = strings.ToLower(k)
	return strings.HasSuffix(k, "url") || strings.HasSuffix(k, "href") || strings.HasSuffix(k, "link")
}

func stripURLFields(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isURLKey(k) {
				if s, ok := val.(string); !ok || !hasAllowedURLPrefix(s) {
					delete(t, k)
				}
				continue
			}
			t[k] = stripURLFields(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = stripURLFields(t[i])
		}
		return t
	}
	return v
}

func hasAllowedURLPrefix(s string) bool {
	for _, p := range allowedRestoredURLPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
