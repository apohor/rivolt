package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
)

// DemoUsername is the public demo account's username (internal/demo). Server
// events are never recorded for it.
const DemoUsername = "rivolt-public-demo"

// ServerEventsEnabled reports whether server events are on: product events
// must be on (RIVOLT_EVENTS, the browser endpoint's switch) AND
// RIVOLT_SERVER_EVENTS=true. The second switch is separate because these
// events are linked to the account and are not subject to the browser's
// Do Not Track / Global Privacy Control signal, so they stay off until the
// privacy policy discloses them.
func ServerEventsEnabled() bool {
	on := func(k string) bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(k)), "true") }
	return on("RIVOLT_EVENTS") && on("RIVOLT_SERVER_EVENTS")
}

// Server-side product events: funnel steps the server sees itself. They go
// into the same events table as the browser's, with anon_id 'server' and the
// user's id as the only link: no IP, location, amount or email. The
// vocabulary is closed (name -> allowed props); anything else is dropped.
const (
	EventSignupComplete     = "signup_complete"
	EventRivianConnectOK    = "rivian_connect_ok"
	EventFirstDriveRecorded = "first_drive_recorded"
)

var serverEventProps = map[string]map[string]func(any) bool{
	EventSignupComplete: {"method": func(v any) bool { s, ok := v.(string); return ok && (s == "oidc" || s == "email") }},
	EventRivianConnectOK: {"vehicle_count": func(v any) bool {
		n, ok := v.(int)
		return ok && n >= 0 && n <= 1_000_000
	}},
	EventFirstDriveRecorded: {},
}

type signupMethodKey struct{}

// WithSignupMethod tags ctx with how the account is being created ("oidc" or
// "email") for the signup_complete event.
func WithSignupMethod(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, signupMethodKey{}, method)
}

func signupMethod(ctx context.Context) string {
	m, _ := ctx.Value(signupMethodKey{}).(string)
	return m
}

func skipServerEvent(d *sql.DB, uid uuid.UUID) bool {
	return d == nil || uid == uuid.Nil || uid == UserIDFor(DemoUsername) || !ServerEventsEnabled()
}

// RecordServerEvent writes one server event for a user. Best effort: a
// failure is logged and never returned, so analytics can't break signup or
// recording. Does nothing when events are off, for the demo user, or for an
// unknown event name; props not allowed for the event are dropped.
func RecordServerEvent(ctx context.Context, d *sql.DB, uid uuid.UUID, name string, props map[string]any) {
	allowed, ok := serverEventProps[name]
	if !ok || skipServerEvent(d, uid) {
		return
	}
	clean := map[string]any{}
	for k, v := range props {
		if valid, ok := allowed[k]; ok && valid(v) {
			clean[k] = v
		}
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO events (anon_id, user_id, name, props) VALUES ('server', $1, $2, $3::jsonb)`,
		uid, name, string(raw)); err != nil {
		slog.Warn("server event failed", "event", name, "err", err.Error())
	}
}

// RecordFirstDrive writes first_drive_recorded when the user's just-closed
// drive is their only one and the event was not written before. Counting the
// user's drives (not the event alone) keeps it once per user after the 90-day
// purge has removed the old event.
func RecordFirstDrive(ctx context.Context, d *sql.DB, uid uuid.UUID) {
	if skipServerEvent(d, uid) {
		return
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO events (anon_id, user_id, name, props)
		SELECT 'server', $1, $2, '{}'::jsonb
		 WHERE (SELECT count(*) FROM drives WHERE user_id = $1) = 1
		   AND NOT EXISTS (SELECT 1 FROM events WHERE user_id = $1 AND name = $2)`,
		uid, EventFirstDriveRecorded); err != nil {
		slog.Warn("server event failed", "event", EventFirstDriveRecorded, "err", err.Error())
	}
}
