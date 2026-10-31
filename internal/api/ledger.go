package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/offpeak"
	"github.com/apohor/rivolt/internal/settings"
)

// maxLedgerMonths bounds /api/ledger/summary so one request can't
// load years of rows.
const maxLedgerMonths = 24

// ledgerAPI holds the ledger handlers' dependencies as seams so tests
// can run them without Postgres.
type ledgerAPI struct {
	load   func(ctx context.Context, uid uuid.UUID, vehicleID string, from, to time.Time) ([]ledger.Charge, []ledger.Drive, error)
	owns   func(ctx context.Context, uid uuid.UUID, vehicleID string) (bool, error)
	config func(ctx context.Context, uid uuid.UUID) (ledger.Config, error)
	now    func() time.Time
	// toConfirmCount counts the user's open verify-state charges; nil
	// leaves the summary counts at zero.
	toConfirmCount func(ctx context.Context, uid uuid.UUID) (open, attention int, err error)
	// billed reports the rivian_receipts flag for a user: when on, the
	// statement carries Rivian's billed amounts (billed_* fields). nil = off.
	billed func(uid uuid.UUID) bool
	// schedule is the user's off-peak electricity schedule (none: no windows).
	schedule func(ctx context.Context, uid uuid.UUID) offpeak.Schedule
	// flatRate reports the user's "my rate is flat" choice (nil: not set): every off-peak estimate stops.
	flatRate func(ctx context.Context, uid uuid.UUID) bool
}

// withBilled returns a copy that adds the billed_* fields for users the
// rivian_receipts flag covers. Only the Money endpoints use it.
func (a ledgerAPI) withBilled(f *flags.Store, sf *settings.Factory) ledgerAPI {
	// The user rides in the context: sf is request-side under row-level security, and a context with no user reads no settings, so
	// every statement would quietly lose its billed amounts.
	a.billed = func(uid uuid.UUID) bool { return receiptsAllowed(auth.WithUser(context.Background(), uid), f, sf, uid) }
	return a
}

// receiptsAllowed is the one check for "may this user's Rivian receipts be used": the
// operator flag covers them AND they opted in. Every use of receipts (the Money
// endpoints, the planner's own-price figure) asks it per request, at call time, rather
// than relying on receipts existing only for opted-in users.
func receiptsAllowed(ctx context.Context, f *flags.Store, sf *settings.Factory, uid uuid.UUID) bool {
	return f != nil && sf != nil && f.RivianReceipts().Enabled(uid.String()) && sf.RivianReceiptsOptIn(ctx, uid)
}

// billedFor is receiptsAllowed for the request's user, for the handlers that
// show a charge: with receipts on, a billed session shows what Rivian billed.
func billedFor(d Deps, uid uuid.UUID) func(context.Context) bool {
	return func(ctx context.Context) bool { return receiptsAllowed(ctx, d.Flags, d.Settings, uid) }
}

// newLedgerAPI wires the production seams: read-only queries on the
// shared pool, the vehicle-ownership check, and the user's charging
// settings (home rate, currency) and home location.
func newLedgerAPI(pool *sql.DB, settingsF *settings.Factory) ledgerAPI {
	return ledgerAPI{
		load: func(ctx context.Context, uid uuid.UUID, vehicleID string, from, to time.Time) ([]ledger.Charge, []ledger.Drive, error) {
			return ledger.Load(ctx, pool, uid, vehicleID, from, to)
		},
		owns: func(ctx context.Context, uid uuid.UUID, vehicleID string) (bool, error) {
			return db.OwnsRivianID(ctx, pool, uid, vehicleID)
		},
		config: func(ctx context.Context, uid uuid.UUID) (ledger.Config, error) {
			var store *settings.Store
			if settingsF != nil {
				store = settingsF.For(uid)
			}
			return ledgerConfig(ctx, store)
		},
		now: time.Now,
		schedule: func(ctx context.Context, uid uuid.UUID) offpeak.Schedule {
			if settingsF == nil {
				return offpeak.Schedule{}
			}
			sch, _, err := settings.GetOffpeak(ctx, settingsF.For(uid))
			if err != nil {
				return offpeak.Schedule{}
			}
			return sch
		},
		flatRate: func(ctx context.Context, uid uuid.UUID) bool {
			if settingsF == nil {
				return false
			}
			on, err := settings.GetFlatRate(ctx, settingsF.For(uid))
			return err == nil && on
		},
		toConfirmCount: func(ctx context.Context, uid uuid.UUID) (int, int, error) {
			return ledger.CountToConfirm(ctx, pool, uid)
		},
	}
}

func ledgerConfig(ctx context.Context, store *settings.Store) (ledger.Config, error) {
	cc, err := settings.GetChargingConfig(ctx, store)
	if err != nil {
		return ledger.Config{}, err
	}
	cfg := ledger.Config{Currency: strings.ToUpper(cc.HomeCurrency), HomeRate: cc.HomePricePerKWh}
	home, err := settings.GetHomeLocation(ctx, store)
	if err != nil {
		return ledger.Config{}, err
	}
	if home.Set {
		cfg.Home = &ledger.LatLon{Lat: home.Latitude, Lon: home.Longitude}
	}
	return cfg, nil
}

// ledgerParams are the query params shared by the ledger endpoints.
type ledgerParams struct {
	loc       *time.Location
	tzSource  string
	vehicleID string
}

func (a ledgerAPI) params(w http.ResponseWriter, r *http.Request, uid uuid.UUID) (ledgerParams, bool) {
	q := r.URL.Query()
	p := ledgerParams{loc: time.UTC, tzSource: "default_utc"}
	if tz := strings.TrimSpace(q.Get("tz")); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil || strings.EqualFold(tz, "local") {
			http.Error(w, "tz must be an IANA time zone, e.g. America/Chicago", http.StatusBadRequest)
			return p, false
		}
		p.loc, p.tzSource = loc, "query"
	}
	p.vehicleID = strings.TrimSpace(q.Get("vehicle_id"))
	if p.vehicleID != "" {
		ok, err := a.owns(r.Context(), uid, p.vehicleID)
		if err != nil {
			http.Error(w, "vehicle lookup failed", http.StatusInternalServerError)
			return p, false
		}
		if !ok {
			http.NotFound(w, r)
			return p, false
		}
	}
	return p, true
}

func (a ledgerAPI) input(ctx context.Context, uid uuid.UUID, p ledgerParams, periods ...ledger.Period) (ledger.Input, error) {
	cfg, err := a.config(ctx, uid)
	if err != nil {
		return ledger.Input{}, err
	}
	cfg.Billed = a.billed != nil && a.billed(uid)
	from, to := ledger.LoadWindow(periods...)
	cs, ds, err := a.load(ctx, uid, p.vehicleID, from, to)
	if err != nil {
		return ledger.Input{}, err
	}
	return ledger.Input{Charges: cs, Drives: ds, Config: cfg, Now: a.now()}, nil
}

func stamp(m *ledger.Monthly, p ledgerParams) {
	m.TZSource = p.tzSource
	if p.vehicleID != "" {
		v := p.vehicleID
		m.VehicleID = &v
	}
}

// handleLedgerEnabled answers the old "show the Money tab?" probe. The tab
// and the current look are always on now; the reply stays only so a tab
// still running an older bundle gets the tab-bar shell and the current look.
func handleLedgerEnabled(_ uuid.UUID, w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": true, "ui_theme_next": true})
}

// monthly serves GET /api/ledger/monthly?month=YYYY-MM[&tz][&vehicle_id].
func (a ledgerAPI) monthly(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	p, ok := a.params(w, r, uid)
	if !ok {
		return
	}
	month := strings.TrimSpace(r.URL.Query().Get("month"))
	if month == "" {
		http.Error(w, "month is required (YYYY-MM)", http.StatusBadRequest)
		return
	}
	period, err := ledger.ParsePeriod(month, p.loc)
	if err != nil {
		http.Error(w, "month must be YYYY-MM", http.StatusBadRequest)
		return
	}
	// A free account's statements are the current month and the two before; an older month is 402 (the rows are kept, not shown).
	if from, on := moneyWindowStart(r.Context(), a.now(), p.loc); on && period.Start.Before(from) {
		writeOutsideFreeWindow(w)
		return
	}
	in, err := a.input(r.Context(), uid, p, period)
	if err != nil {
		http.Error(w, "ledger: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := ledger.Build(in, period, true)
	stamp(&out, p)
	writeJSON(w, http.StatusOK, out)
}

// ledgerSummaryResponse is the GET /api/ledger/summary body.
type ledgerSummaryResponse struct {
	Version  int                   `json:"version"`
	TZ       string                `json:"tz"`
	Currency string                `json:"currency"`
	Months   []ledger.MonthSummary `json:"months"`
	// ToConfirmCount / NeedsAttentionCount are all-time and user-wide
	// (not windowed by months or vehicle): charges whose auto-detected
	// DC fast charge network is still open for verification.
	ToConfirmCount      int `json:"to_confirm_count"`
	NeedsAttentionCount int `json:"needs_attention_count"`
}

// summary serves GET /api/ledger/summary?months=N[&tz][&vehicle_id]:
// the last N months (oldest first) without receipts.
func (a ledgerAPI) summary(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	p, ok := a.params(w, r, uid)
	if !ok {
		return
	}
	n := 12
	if v := strings.TrimSpace(r.URL.Query().Get("months")); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 || parsed > maxLedgerMonths {
			http.Error(w, "months must be 1-24", http.StatusBadRequest)
			return
		}
		n = parsed
	}
	// A free account's list stops at the window: the current month and the two before, however many were asked for.
	if _, on := moneyWindowStart(r.Context(), a.now(), p.loc); on && n > 3 {
		n = 3
	}
	periods := ledger.LastMonths(a.now(), n, p.loc)
	in, err := a.input(r.Context(), uid, p, periods...)
	if err != nil {
		http.Error(w, "ledger: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := ledgerSummaryResponse{Version: ledger.Version, TZ: p.loc.String(), Currency: in.Config.Currency}
	for _, period := range periods {
		m := ledger.Build(in, period, false)
		stamp(&m, p)
		out.Months = append(out.Months, ledger.MonthSummary{Monthly: m})
	}
	if a.toConfirmCount != nil {
		open, attention, err := a.toConfirmCount(r.Context(), uid)
		if err != nil {
			http.Error(w, "ledger: "+err.Error(), http.StatusInternalServerError)
			return
		}
		out.ToConfirmCount, out.NeedsAttentionCount = open, attention
	}
	writeJSON(w, http.StatusOK, out)
}

// flagsAudienceRequest is the PUT body for an audience flag
// (/api/admin/flags/statement-email): everyone, or the listed users.
type flagsAudienceRequest struct {
	All   bool     `json:"all"`
	Users []string `json:"users"`
}

// handleFlagsStatementEmailPut sets which users the monthly statement
// email is enabled for (operator flag; each user must also opt in).
func handleFlagsStatementEmailPut(store *flags.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "flags store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req flagsAudienceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		users := make([]string, 0, len(req.Users))
		for _, u := range req.Users {
			id, err := uuid.Parse(strings.TrimSpace(u))
			if err != nil {
				http.Error(w, "users must be user UUIDs", http.StatusBadRequest)
				return
			}
			users = append(users, id.String())
		}
		actor := "admin"
		if uid, ok := auth.UserFromContext(r.Context()); ok {
			actor = uid.String()
		}
		if err := store.SetStatementEmail(r.Context(), flags.AudienceState{All: req.All, Users: users}, actor); err != nil {
			http.Error(w, "set flag: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"statement_email": store.StatementEmail()})
	}
}
