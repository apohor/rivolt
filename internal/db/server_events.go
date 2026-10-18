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
// RIVOLT_SERVER_EVENTS=true. The second switch is separate and defaults off
// as a safety net, so these account-linked events start only when ops flips
// it.
func ServerEventsEnabled() bool {
	on := func(k string) bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(k)), "true") }
	return on("RIVOLT_EVENTS") && on("RIVOLT_SERVER_EVENTS")
}

type doNotTrackKey struct{}

// WithDoNotTrack marks ctx as belonging to a request whose browser sent
// Do Not Track or Global Privacy Control. Server events recorded under it
// are dropped, the same promise the browser endpoint keeps.
func WithDoNotTrack(ctx context.Context) context.Context {
	return context.WithValue(ctx, doNotTrackKey{}, true)
}

// DoNotTrack reports whether ctx carries the opt-out.
func DoNotTrack(ctx context.Context) bool {
	v, _ := ctx.Value(doNotTrackKey{}).(bool)
	return v
}

// Server-side product events: funnel steps the server sees itself. They go
// into the same events table as the browser's, with anon_id 'server' and the
// user's id as the only link: no IP, location, amount or email. The
// vocabulary is closed (name -> allowed props); anything else is dropped.
const (
	EventSignupComplete  = "signup_complete"
	EventRivianConnectOK = "rivian_connect_ok"
)

var serverEventProps = map[string]map[string]func(any) bool{
	EventSignupComplete: {"method": func(v any) bool { s, ok := v.(string); return ok && (s == "oidc" || s == "email") }},
	EventRivianConnectOK: {"vehicle_count": func(v any) bool {
		n, ok := v.(int)
		return ok && n >= 0 && n <= 1_000_000
	}},
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

// Outcomes of RecordServerEvent, for ServerEventObserver.
const (
	ServerEventRecorded        = "recorded"
	ServerEventSkippedDNT      = "skipped_dnt"      // the request carried Do Not Track / Global Privacy Control
	ServerEventSkippedDisabled = "skipped_disabled" // events or server events are off
	ServerEventSkippedOther    = "skipped_other"    // demo user, no user, no database
	ServerEventFailed          = "failed"           // the insert failed
)

// ServerEventObserver, when set (once, at startup, before serving), is told
// the outcome of every RecordServerEvent call: a metrics hook that makes
// "why is there no signup_complete row?" answerable. It sees the event name
// and the outcome, never a user id.
var ServerEventObserver func(event, outcome string)

func observeServerEvent(event, outcome string) {
	if ServerEventObserver != nil {
		ServerEventObserver(event, outcome)
	}
}

// RecordServerEvent writes one server event for a user. Best effort: a
// failure is logged and never returned, so analytics can't break signup or
// recording. Does nothing when events are off, for the demo user, or for an
// unknown event name; props not allowed for the event are dropped.
func RecordServerEvent(ctx context.Context, d *sql.DB, uid uuid.UUID, name string, props map[string]any) {
	allowed, ok := serverEventProps[name]
	if !ok {
		return
	}
	switch {
	case !ServerEventsEnabled():
		observeServerEvent(name, ServerEventSkippedDisabled)
		return
	case DoNotTrack(ctx):
		observeServerEvent(name, ServerEventSkippedDNT)
		return
	case skipServerEvent(d, uid):
		observeServerEvent(name, ServerEventSkippedOther)
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
		observeServerEvent(name, ServerEventFailed)
		return
	}
	observeServerEvent(name, ServerEventRecorded)
}
