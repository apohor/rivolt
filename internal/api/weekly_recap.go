package api

import (
	"context"
	"database/sql"
	"html"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/statement"
	"github.com/apohor/rivolt/internal/vehiclelease"
	"github.com/apohor/rivolt/internal/weekly"
)

// The weekly recap email (internal/weekly): the figures come from the ledger
// over the previous local week, the same code path as the Money tab.

var weekKeyRE = regexp.MustCompile(`^weekly:[0-9]{4}-W(0[1-9]|[1-4][0-9]|5[0-3])$`)

// newWeeklySummary computes a week's Summary for an account.
func newWeeklySummary(la ledgerAPI, pool *sql.DB) weekly.SummaryFunc {
	return func(ctx context.Context, c weekly.Candidate, start, end time.Time, loc *time.Location) (weekly.Summary, error) {
		p := ledger.Period{Month: start.In(loc).Format("2006-01-02"), Loc: loc, Start: start, End: end}
		in, err := la.input(ctx, c.User, ledgerParams{loc: loc, tzSource: "weekly_recap"}, p)
		if err != nil {
			return weekly.Summary{}, err
		}
		m := ledger.Build(in, p, false)
		s := weekly.Summary{
			Model: c.Model, Currency: m.Currency,
			Miles: m.Driving.Miles, Drives: m.Driving.Drives,
			KWhCharged: m.Energy.KWhAdded, Cost: m.Spend.Total, Charges: m.Energy.Sessions,
		}
		if m.Driving.KWhUsedKnown > 0 && m.Driving.MilesWithEnergy > 0 {
			s.KWhUsed = m.Driving.KWhUsedKnown
			s.MiPerKWh = m.Driving.MilesWithEnergy / m.Driving.KWhUsedKnown
		}
		if in.Config.Home != nil && m.Energy.KWhAdded > 0 {
			pct := int(math.Round(100 * m.Energy.ByPlace["home"].KWh / m.Energy.KWhAdded))
			s.HomePct = &pct
		}
		if s.Drives >= 2 {
			s.BestMiles, s.BestMiPerKWh = bestDrive(in.Drives, start, end)
		}
		s.Lease = leasePace(ctx, pool, c.User, time.Now())
		return s, nil
	}
}

// bestDrive is the most efficient drive of the week among those long enough to
// mean something (and with a known energy use).
func bestDrive(all []ledger.Drive, start, end time.Time) (miles, miPerKWh float64) {
	ds, _ := ledger.EffectiveDrives(all)
	for _, d := range ds {
		if d.StartedAt.Before(start) || !d.StartedAt.Before(end) || d.DistanceMi == nil || d.EnergyKWh == nil {
			continue
		}
		if *d.DistanceMi < weekly.BestDriveMinMiles || *d.EnergyKWh <= 0 {
			continue
		}
		if e := *d.DistanceMi / *d.EnergyKWh; e > miPerKWh {
			miles, miPerKWh = *d.DistanceMi, e
		}
	}
	return miles, miPerKWh
}

// leasePace reads the first lease the account tracks; nil without one or
// without a recent odometer reading.
func leasePace(ctx context.Context, pool *sql.DB, uid uuid.UUID, now time.Time) *weekly.LeasePace {
	rows, err := pool.QueryContext(ctx, `SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id NOT LIKE 'electrafi-%' ORDER BY created_at`, uid)
	if err != nil {
		return nil
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	st := vehiclelease.NewStore(pool)
	for _, vid := range ids {
		t, err := st.Get(ctx, uid, vid)
		if err != nil || t == nil {
			continue
		}
		latest, past, err := st.Odometer(ctx, uid, vid, now)
		if err != nil {
			continue
		}
		out, err := vehiclelease.Compute(*t, now, latest, past)
		if err != nil || out.AheadBehindMi == nil {
			return nil
		}
		return &weekly.LeasePace{MilesOver: -*out.AheadBehindMi}
	}
	return nil
}

// emailSweepsAllowed says which background email sweeps (the monthly statement,
// the weekly recap) this deployment runs. They are background work: never on the
// admin deployment, whatever secrets it has mounted; claims would stop duplicate
// mail, but the admin pods are not senders.
func emailSweepsAllowed(d Deps) (statement, weekly bool) {
	if d.AdminOnly {
		return false, false
	}
	statement = d.DB != nil && d.StatementSigner != nil && d.Email != nil && d.Flags != nil
	weekly = d.DB != nil && d.WeeklySigner != nil && d.Email != nil && !demo.OnlyEnabled()
	return statement, weekly
}

// startWeeklyJob runs the recap sweep for the life of the process. Safe on every
// replica: the engage claim makes each (account, week) once.
func startWeeklyJob(d Deps) {
	j := &weekly.Job{
		Source:    weekly.NewSource(d.DB, demo.UserID()),
		Summarize: newWeeklySummary(newLedgerAPI(d.DB, d.Settings), d.DB),
		Mailer:    d.Email,
		Signer:    d.WeeklySigner,
		BaseURL:   d.BaseURL,
		Logger:    d.Logger,
		Engage: &engage.Engage{Store: engage.NewStore(d.DB), Observe: func(kind, outcome string) {
			if d.Metrics != nil {
				d.Metrics.EngagementSendsTotal.WithLabelValues(kind, outcome).Inc()
			}
		}},
	}
	go func() {
		ctx := context.Background()
		j.Run(ctx)
	}()
	slog.Info("weekly recap job started")
}

type weeklyUnsub struct {
	db     *sql.DB
	signer *statement.Signer
	now    func() time.Time
}

func (a *weeklyUnsub) verify(r *http.Request) (uuid.UUID, bool) {
	uid, key, err := a.signer.Verify(r.URL.Query().Get("t"), a.now())
	if err != nil || !weekKeyRE.MatchString(key) {
		return uuid.Nil, false
	}
	return uid, true
}

const weeklyExpired = `<h1>This link has expired</h1><p>Sign in to Rivolt and change the weekly recap in Settings → Notifications.</p>`

// GET shows a confirmation page whose form POSTs; link scanners that only GET
// never unsubscribe anyone by accident.
func (a *weeklyUnsub) get(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.verify(r); !ok {
		writeUnsubPage(w, http.StatusBadRequest, weeklyExpired)
		return
	}
	writeUnsubPage(w, http.StatusOK, `<h1>Stop the weekly recap?</h1>
<form method="post" action="/api/weekly-recap/unsubscribe?t=`+html.EscapeString(r.URL.Query().Get("t"))+`"><button type="submit">Unsubscribe</button></form>`)
}

// POST performs it (RFC 8058 one-click posts here too). Idempotent.
func (a *weeklyUnsub) post(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.verify(r)
	if !ok {
		writeUnsubPage(w, http.StatusBadRequest, weeklyExpired)
		return
	}
	if _, err := a.db.ExecContext(r.Context(),
		`UPDATE engagement_prefs SET weekly_recap = FALSE, updated_at = NOW() WHERE user_id = $1 AND weekly_recap`, uid); err != nil {
		serverError(w, r, "weekly recap unsubscribe", err)
		return
	}
	writeUnsubPage(w, http.StatusOK, `<h1>You're unsubscribed from the weekly recap</h1><p>You can turn it back on in Settings → Notifications.</p>`)
}
