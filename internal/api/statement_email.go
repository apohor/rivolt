package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/offpeak"
	"github.com/apohor/rivolt/internal/statement"
)

// Monthly statement email API (scratchpad/statement-email-spec.md):
// the user's opt-in and timezone, the preview, and the public signed
// one-click unsubscribe. Off by default twice: the operator flag
// (statement_email_enabled) and the user's own opt-in.

var periodRE = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

type statementAPI struct {
	store   statement.Store
	flag    func(userID string) bool
	monthly statement.MonthlyFunc
	savings statement.SavingsFunc
	job     *statement.Job // Build only: preview and send share one renderer
	signer  *statement.Signer
	now     func() time.Time
	// scope says what a user's figures cover (see proVehicles.statementScope); the preview and the job use the same one.
	scope func(ctx context.Context, uid uuid.UUID) (vehicleID, note string)
}

// newStatementMonthly adapts the ledger API's loader: the same
// in-process path as GET /api/ledger/monthly, so the email's totals are
// the Money tab's totals.
func newStatementMonthly(la ledgerAPI, pv *proVehicles) statement.MonthlyFunc {
	return func(ctx context.Context, uid uuid.UUID, period string, loc *time.Location) (ledger.Monthly, error) {
		p, err := ledger.ParsePeriod(period, loc)
		if err != nil {
			return ledger.Monthly{}, err
		}
		vehicle, _ := pv.statementScope(ctx, uid)
		in, err := la.input(ctx, uid, ledgerParams{loc: loc, tzSource: "statement_email", vehicleID: vehicle}, p)
		if err != nil {
			return ledger.Monthly{}, err
		}
		return ledger.Build(in, p, true), nil
	}
}

// newStatementSavings is the off-peak saving estimate for the statement email: the
// same code as GET /api/ledger/savings for that month and the user's own schedule.
func newStatementSavings(la ledgerAPI, pv *proVehicles) statement.SavingsFunc {
	return func(ctx context.Context, uid uuid.UUID, period string, loc *time.Location) (*ledger.Savings, error) {
		p, err := ledger.ParsePeriod(period, loc)
		if err != nil {
			return nil, err
		}
		vehicle, _ := pv.statementScope(ctx, uid)
		in, err := la.input(ctx, uid, ledgerParams{loc: loc, tzSource: "statement_email", vehicleID: vehicle}, p)
		if err != nil {
			return nil, err
		}
		// A flat rate has no off-peak hours: no estimate, so no saving line in the email.
		if la.flatRate != nil && la.flatRate(ctx, uid) {
			return nil, nil
		}
		var sch offpeak.Schedule
		if la.schedule != nil {
			sch = la.schedule(ctx, uid)
		}
		s := ledger.EstimateSavings(ledger.HomeACSessions(in, p), sch, in.Config.HomeRate)
		return &s, nil
	}
}

func newStatementAPI(pool *sql.DB, d Deps, signer *statement.Signer) *statementAPI {
	store := statement.NewStore(pool)
	la := newLedgerAPI(pool, d.Settings)
	pv := &proVehicles{db: pool, settings: d.Settings, ent: d.Entitlements, log: d.Logger}
	flag := func(string) bool { return false }
	if d.Flags != nil {
		flag = func(uid string) bool { return d.Flags.StatementEmail().Enabled(uid) }
	}
	return &statementAPI{
		store: store, flag: flag, signer: signer, now: time.Now,
		monthly: newStatementMonthly(la, pv),
		savings: newStatementSavings(la, pv),
		scope:   pv.statementScope,
		job:     &statement.Job{Store: store, Signer: signer, BaseURL: d.BaseURL, Now: time.Now},
	}
}

type statementSettings struct {
	Enabled        bool   `json:"enabled"`
	Timezone       string `json:"timezone"`
	Available      bool   `json:"available"`
	LastSentPeriod string `json:"last_sent_period"`
}

func (a *statementAPI) available(r *http.Request, uid uuid.UUID) (bool, string, error) {
	if !a.flag(uid.String()) {
		return false, "", nil
	}
	e, err := a.store.Email(r.Context(), uid)
	if err != nil {
		return false, "", err
	}
	return strings.TrimSpace(e) != "", e, nil
}

// GET /api/settings/statement-email
func (a *statementAPI) getSettings(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	p, err := a.store.GetPrefs(r.Context(), uid)
	if err != nil {
		serverError(w, r, "statement settings", err)
		return
	}
	avail, _, err := a.available(r, uid)
	if err != nil {
		serverError(w, r, "statement settings", err)
		return
	}
	last, err := a.store.LastSentPeriod(r.Context(), uid)
	if err != nil {
		serverError(w, r, "statement settings", err)
		return
	}
	writeJSON(w, http.StatusOK, statementSettings{Enabled: p.Enabled, Timezone: p.Timezone, Available: avail, LastSentPeriod: last})
}

// PUT /api/settings/statement-email {enabled, timezone}
func (a *statementAPI) putSettings(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	// Opting in to mail is the user's own act, never an admin's while
	// viewing as them.
	if auth.IsImpersonating(r.Context()) {
		http.Error(w, "not available while viewing as another user", http.StatusForbidden)
		return
	}
	var body struct {
		Enabled  bool   `json:"enabled"`
		Timezone string `json:"timezone"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	tz := strings.TrimSpace(body.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil || strings.EqualFold(tz, "local") {
		http.Error(w, "timezone must be an IANA time zone, e.g. America/Chicago", http.StatusBadRequest)
		return
	}
	avail, _, err := a.available(r, uid)
	if err != nil {
		serverError(w, r, "statement settings", err)
		return
	}
	// Turning it ON needs the feature available; turning it OFF is
	// always allowed.
	if body.Enabled && !avail {
		if !a.flag(uid.String()) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "no email address on this account", http.StatusConflict)
		return
	}
	if err := a.store.SetPrefs(r.Context(), uid, statement.Prefs{Enabled: body.Enabled, Timezone: tz}); err != nil {
		serverError(w, r, "statement settings", err)
		return
	}
	a.getSettings(uid, w, r)
}

// GET /api/statement-email/preview?month=YYYY-MM
//
// Renders the same message the job would send, with the user's own
// data, and never sends it. 404 when the operator flag is off.
func (a *statementAPI) preview(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	if !a.flag(uid.String()) {
		http.NotFound(w, r)
		return
	}
	prefs, err := a.store.GetPrefs(r.Context(), uid)
	if err != nil {
		serverError(w, r, "statement preview", err)
		return
	}
	loc, lerr := time.LoadLocation(prefs.Timezone)
	if lerr != nil {
		loc = time.UTC
	}
	period := strings.TrimSpace(r.URL.Query().Get("month"))
	if period == "" {
		period = statement.PreviousMonth(a.now(), loc)
	}
	if !periodRE.MatchString(period) {
		http.Error(w, "month must be YYYY-MM", http.StatusBadRequest)
		return
	}
	// A free account previews the months its Money tab shows. The email job itself is not windowed (it covers last month, which is inside).
	from, windowed := moneyWindowStart(r.Context(), a.now(), loc)
	if t, perr := time.ParseInLocation("2006-01", period, loc); windowed && perr == nil && t.Before(from) {
		writeOutsideFreeWindow(w)
		return
	}
	cur, err := a.monthly(r.Context(), uid, period, loc)
	if err != nil {
		serverError(w, r, "statement preview", err)
		return
	}
	var prev *ledger.Monthly
	if t, perr := time.ParseInLocation("2006-01", period, loc); perr == nil {
		// The month before is the comparison; for a free account it is left out when it falls outside the window.
		if pt := t.AddDate(0, -1, 0); !windowed || !pt.Before(from) {
			if p, perr := a.monthly(r.Context(), uid, pt.Format("2006-01"), loc); perr == nil {
				prev = &p
			}
		}
	}
	addr, err := a.store.Email(r.Context(), uid)
	if err != nil {
		serverError(w, r, "statement preview", err)
		return
	}
	cand := statement.Candidate{UserID: uid, Timezone: prefs.Timezone, Email: addr}
	if a.scope != nil {
		_, cand.Note = a.scope(r.Context(), uid)
	}
	var sv *ledger.Savings
	if a.savings != nil {
		if s, serr := a.savings(r.Context(), uid, period, loc); serr == nil {
			sv = s
		}
	}
	msg, ok := a.job.BuildWith(cand, period, cur, prev, sv)
	out := map[string]any{"will_send": false}
	switch {
	case !ok:
		out["reason"] = "silent_month"
	default:
		out["subject"], out["text"], out["html"] = msg.Subject, msg.Text, msg.HTML
		switch {
		case !prefs.Enabled:
			out["reason"] = "not_opted_in"
		case strings.TrimSpace(addr) == "":
			out["reason"] = "no_email"
		default:
			out["will_send"] = true
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// ---- unsubscribe (public, no session) --------------------------------

const unsubPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>Rivolt</title><style>body{font:16px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;max-width:480px;margin:12vh auto;padding:0 20px;color:#111;background:#fff}@media(prefers-color-scheme:dark){body{background:#0b0b0c;color:#eee}}button{font:inherit;padding:10px 16px;border-radius:8px;border:0;background:#059669;color:#fff;cursor:pointer}</style></head><body>%s</body></html>`

func writeUnsubPage(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	fmt.Fprintf(w, unsubPage, body)
}

// GET shows a confirmation page whose form POSTs; link scanners that
// only GET never unsubscribe anyone by accident.
func (a *statementAPI) unsubscribeGet(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("t")
	if _, period, err := a.signer.Verify(tok, a.now()); err != nil || !periodRE.MatchString(period) {
		writeUnsubPage(w, http.StatusBadRequest, `<h1>This link has expired</h1><p>Sign in to Rivolt and change monthly statements in Settings → Notifications.</p>`)
		return
	}
	writeUnsubPage(w, http.StatusOK, `<h1>Unsubscribe from monthly statements?</h1>
<form method="post" action="/api/statement-email/unsubscribe?t=`+html.EscapeString(tok)+`"><button type="submit">Unsubscribe</button></form>`)
}

// POST performs it (RFC 8058 one-click posts here too). Idempotent:
// an already-unsubscribed user gets the same success page.
func (a *statementAPI) unsubscribePost(w http.ResponseWriter, r *http.Request) {
	uid, period, err := a.signer.Verify(r.URL.Query().Get("t"), a.now())
	if err != nil || !periodRE.MatchString(period) {
		writeUnsubPage(w, http.StatusBadRequest, `<h1>This link has expired</h1><p>Sign in to Rivolt and change monthly statements in Settings → Notifications.</p>`)
		return
	}
	prefs, err := a.store.GetPrefs(r.Context(), uid)
	if err != nil {
		serverError(w, r, "statement unsubscribe", err)
		return
	}
	if prefs.Enabled {
		prefs.Enabled = false
		if err := a.store.SetPrefs(r.Context(), uid, prefs); err != nil {
			serverError(w, r, "statement unsubscribe", err)
			return
		}
	}
	writeUnsubPage(w, http.StatusOK, `<h1>You're unsubscribed from monthly statements</h1><p>You can turn them back on in Settings → Notifications.</p>`)
}

// serverError logs the error and answers a generic 500 (store and SQL
// text stay out of response bodies).
func serverError(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), what, "err", err.Error(), "path", r.URL.Path)
	http.Error(w, "unavailable", http.StatusInternalServerError)
}

// startStatementJob runs the monthly statement email job hourly for
// the life of the process. It does nothing useful unless the operator
// flag is on and users opted in (the job itself checks both), and it is
// safe on every replica: the (user, period) claim row is the
// idempotency key.
func startStatementJob(d Deps, signer *statement.Signer) {
	// System pool: a sweep across every opted-in user, with no request and so no user in context. Its settings reads go through the
	// system-side factory for the same reason.
	la := newLedgerAPI(d.sysDB(), d.systemSettings())
	pv := &proVehicles{db: d.sysDB(), settings: d.systemSettings(), ent: d.Entitlements, log: d.Logger}
	j := &statement.Job{
		Store:   statement.NewStore(d.sysDB()),
		Mailer:  d.Email,
		Monthly: newStatementMonthly(la, pv),
		Savings: newStatementSavings(la, pv),
		NoteFor: func(ctx context.Context, uid uuid.UUID) string { _, n := pv.statementScope(ctx, uid); return n },
		Signer:  signer,
		BaseURL: d.BaseURL,
		Logger:  d.Logger,
		Enabled: func(uid string) bool { return d.Flags.StatementEmail().Enabled(uid) },
	}
	go func() {
		t := time.NewTimer(time.Minute) // let the pod settle first
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
			st, err := j.RunOnce(ctx)
			cancel()
			if err != nil {
				slog.Warn("statement email run failed", "err", err.Error())
			} else if st.Sent+st.Failed+st.Skipped > 0 {
				slog.Info("statement email run", "considered", st.Considered, "due", st.Due,
					"sent", st.Sent, "failed", st.Failed, "skipped", st.Skipped, "already_done", st.AlreadyDone)
			}
			t.Reset(time.Hour)
		}
	}()
}
