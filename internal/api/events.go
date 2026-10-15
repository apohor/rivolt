package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

// Product events (docs/product/instrumentation.md). First-party only,
// counts and enums, no IP or User-Agent stored. Off unless
// RIVOLT_EVENTS=true.
const (
	eventsMaxBody     = 2048 // bytes
	eventsMaxBatch    = 20
	eventsRetention   = 90 * 24 * time.Hour
	eventsPurgeEvery  = time.Hour
	eventsRatePerMin  = 60
	eventsMaxTokenLen = 40
)

// eventsEnabled reports whether the events endpoint is on.
func eventsEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("RIVOLT_EVENTS")), "true")
}

type propKind int

const (
	propToken propKind = iota // [a-z0-9_.-], <= eventsMaxTokenLen
	propInt                   // 0..1_000_000
	propBool
	propEnum
)

type propSpec struct {
	kind propKind
	enum []string
}

var (
	tokenProp = propSpec{kind: propToken}
	intProp   = propSpec{kind: propInt}
	boolProp  = propSpec{kind: propBool}
)

func enumProp(v ...string) propSpec { return propSpec{kind: propEnum, enum: v} }

// eventAllowlist is the whole vocabulary: event name -> allowed
// property keys. Unknown names reject the batch; unknown property keys
// and invalid values are dropped.
var eventAllowlist = map[string]map[string]propSpec{
	"landing_view":           {"referrer_host": tokenProp, "utm_source": tokenProp},
	"demo_view":              {"tab": tokenProp},
	"demo_tab":               {"tab": tokenProp},
	"signup_start":           {"method": enumProp("oidc", "email")},
	"signup_done":            {"method": enumProp("oidc", "email")},
	"rivian_connect_start":   {"authorized_driver": boolProp},
	"rivian_connect_done":    {"vehicle_count": intProp},
	"rivian_connect_fail":    {"class": enumProp("user-action", "mfa", "rate", "other")},
	"first_data":             {"seconds_since_connect": intProp},
	"money_view":             {"month_offset": intProp, "src": enumProp("statement")},
	"receipt_open":           {"source": enumProp("billed", "est_home", "est_network", "user")},
	"price_edit":             {"from_source": tokenProp},
	"statement_share":        {"kind": enumProp("png", "csv")},
	"statement_email_optin":  {"from": enumProp("settings", "email")},
	"statement_email_optout": {"from": enumProp("settings", "email")},
	"plan_run":               {"stops_bucket": tokenProp},
	"alert_shown":            {"kind": tokenProp},
	"alert_dismiss":          {"kind": tokenProp},
}

var (
	anonIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	tokenRe  = regexp.MustCompile(`^[a-z0-9_.-]{1,40}$`)
)

type eventIn struct {
	Name  string         `json:"name"`
	Props map[string]any `json:"props"`
}

type eventsRequest struct {
	AnonID string    `json:"anon_id"`
	Events []eventIn `json:"events"`
}

// storedEvent is a validated event ready to insert.
type storedEvent struct {
	Name  string
	Props map[string]any
}

// sanitizeProps keeps only allowlisted keys with valid values.
func sanitizeProps(name string, in map[string]any) map[string]any {
	out := map[string]any{}
	spec := eventAllowlist[name]
	for k, v := range in {
		ps, ok := spec[k]
		if !ok {
			continue
		}
		switch ps.kind {
		case propToken:
			if s, ok := v.(string); ok && tokenRe.MatchString(s) {
				out[k] = s
			}
		case propEnum:
			if s, ok := v.(string); ok {
				for _, e := range ps.enum {
					if s == e {
						out[k] = s
					}
				}
			}
		case propBool:
			if b, ok := v.(bool); ok {
				out[k] = b
			}
		case propInt:
			if f, ok := v.(float64); ok && f >= 0 && f <= 1_000_000 && f == float64(int64(f)) {
				out[k] = int64(f)
			}
		}
	}
	return out
}

// eventsSink persists validated events.
type eventsSink interface {
	Insert(ctx context.Context, anonID string, userID *uuid.UUID, evs []storedEvent) error
}

// handleEvents serves POST /api/events. Public (session optional),
// per-IP rate limited by the caller's middleware, body capped at 2 KB.
// Honours Do-Not-Track by storing nothing.
func handleEvents(sink eventsSink) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("DNT") == "1" || r.Header.Get("Sec-GPC") == "1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req eventsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if !anonIDRe.MatchString(req.AnonID) {
			http.Error(w, "anon_id must be 16-64 chars of [A-Za-z0-9_-]", http.StatusBadRequest)
			return
		}
		if len(req.Events) == 0 || len(req.Events) > eventsMaxBatch {
			http.Error(w, "events must hold 1-20 items", http.StatusBadRequest)
			return
		}
		evs := make([]storedEvent, 0, len(req.Events))
		for _, e := range req.Events {
			if _, ok := eventAllowlist[e.Name]; !ok {
				http.Error(w, "unknown event name", http.StatusBadRequest)
				return
			}
			evs = append(evs, storedEvent{Name: e.Name, Props: sanitizeProps(e.Name, e.Props)})
		}
		var uid *uuid.UUID
		if u, ok := auth.UserFromContext(r.Context()); ok && u != uuid.Nil {
			uid = &u
		}
		if err := sink.Insert(r.Context(), req.AnonID, uid, evs); err != nil {
			http.Error(w, "events unavailable", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// pgEventsSink writes to the events table.
type pgEventsSink struct{ db *sql.DB }

func newPGEventsSink(db *sql.DB) *pgEventsSink { return &pgEventsSink{db: db} }

func (s *pgEventsSink) Insert(ctx context.Context, anonID string, userID *uuid.UUID, evs []storedEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var uidArg any
	if userID != nil {
		uidArg = *userID
	}
	for _, e := range evs {
		props, err := json.Marshal(e.Props)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO events (anon_id, user_id, name, props) VALUES ($1, $2, $3, $4::jsonb)`,
			anonID, uidArg, e.Name, string(props)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// purgeEvents deletes events older than the retention window.
func purgeEvents(ctx context.Context, db *sql.DB, now time.Time) error {
	_, err := db.ExecContext(ctx, `DELETE FROM events WHERE ts < $1::timestamptz`, now.Add(-eventsRetention).UTC())
	return err
}

// startEventsPurge purges at boot and then hourly for the life of the
// process. It runs whether or not RIVOLT_EVENTS is on, so rows never
// outlive the 90-day promise after the flag is turned off.
func startEventsPurge(db *sql.DB) {
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := purgeEvents(ctx, db, time.Now()); err != nil {
			slog.Warn("events purge failed", "err", err)
		}
	}
	go func() {
		run()
		t := time.NewTicker(eventsPurgeEvery)
		defer t.Stop()
		for range t.C {
			run()
		}
	}()
}
