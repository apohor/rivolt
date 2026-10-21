package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/offpeak"
)

// maxSavingsMonths bounds how far back one savings request looks.
const maxSavingsMonths = 12

// savingsBasis is the sentence the card shows under the figure.
const savingsBasis = "Each home charge is priced again at the cheapest of your off-peak windows that its plug-in time could have held in full, from your own sessions only. It is an estimate: your real bill can differ, and charges that could not move are left out."

// savingsResponse is GET /api/ledger/savings.
type savingsResponse struct {
	Version  int      `json:"version"`
	TZ       string   `json:"tz"`
	Currency string   `json:"currency"`
	Months   []string `json:"months"`
	// ScheduleTZ is the zone the user's off-peak times are read in.
	ScheduleTZ string  `json:"schedule_tz,omitempty"`
	BaseRate   float64 `json:"base_rate"`
	ledger.Savings
	Basis string `json:"basis"`
}

// savings serves GET /api/ledger/savings?[month=YYYY-MM][&months=N][&tz][&vehicle_id]:
// what moving the user's home charging to their cheapest off-peak hours could
// have saved over the N months ending at month (default: the last complete month,
// one month). The figure is absent, with "needs", until the home rate and an
// off-peak schedule are set and there are a few home sessions to look at.
func (a ledgerAPI) savings(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	var sch offpeak.Schedule
	if a.schedule != nil {
		sch = a.schedule(r.Context(), uid)
	}
	p, ok := a.params(w, r, uid)
	if !ok {
		return
	}
	// With no tz asked for, months are cut in the zone the schedule is read in.
	if p.tzSource == "default_utc" && sch.TZ != "" {
		if loc, err := time.LoadLocation(sch.TZ); err == nil {
			p.loc, p.tzSource = loc, "schedule"
		}
	}
	n := 1
	if v := strings.TrimSpace(r.URL.Query().Get("months")); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 || parsed > maxSavingsMonths {
			http.Error(w, "months must be 1-12", http.StatusBadRequest)
			return
		}
		n = parsed
	}
	var endMonth time.Time
	if m := strings.TrimSpace(r.URL.Query().Get("month")); m != "" {
		pp, err := ledger.ParsePeriod(m, p.loc)
		if err != nil {
			http.Error(w, "month must be YYYY-MM", http.StatusBadRequest)
			return
		}
		endMonth = pp.Start.In(p.loc)
	} else {
		cur := a.now().In(p.loc)
		endMonth = time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, p.loc).AddDate(0, -1, 0)
	}
	var periods []ledger.Period
	var names []string
	for i := n - 1; i >= 0; i-- {
		first := time.Date(endMonth.Year(), endMonth.Month(), 1, 0, 0, 0, 0, p.loc).AddDate(0, -i, 0)
		pp, err := ledger.ParsePeriod(first.Format("2006-01"), p.loc)
		if err != nil {
			http.Error(w, "month must be YYYY-MM", http.StatusBadRequest)
			return
		}
		periods = append(periods, pp)
		names = append(names, pp.Month)
	}
	in, err := a.input(r.Context(), uid, p, periods...)
	if err != nil {
		http.Error(w, "ledger: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var sessions []ledger.HomeSession
	for _, pp := range periods {
		sessions = append(sessions, ledger.HomeACSessions(in, pp)...)
	}
	out := savingsResponse{
		Version: ledger.Version, TZ: p.loc.String(), Currency: in.Config.Currency, Months: names,
		ScheduleTZ: sch.TZ, BaseRate: in.Config.HomeRate,
		Savings: ledger.EstimateSavings(sessions, sch, in.Config.HomeRate), Basis: savingsBasis,
	}
	writeJSON(w, http.StatusOK, out)
}
