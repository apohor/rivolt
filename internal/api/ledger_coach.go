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

// defaultCoachMonths is the window GET /api/ledger/coach looks at when none is asked for: long enough for the figures to settle.
const defaultCoachMonths = 3

// coach serves GET /api/ledger/coach?[months=N][&tz][&vehicle_id]: the savings coach over the N months ending with the current one
// (default 3, at most 12), clipped to a free account's history window like the other Money reads. Computed on request from stored charges
// and drives; nothing is fetched from Rivian and nothing is recorded.
func (a ledgerAPI) coach(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	p, ok := a.params(w, r, uid)
	if !ok {
		return
	}
	var sch offpeak.Schedule
	if a.schedule != nil {
		sch = a.schedule(r.Context(), uid)
	}
	if p.tzSource == "default_utc" && sch.TZ != "" {
		if loc, err := time.LoadLocation(sch.TZ); err == nil {
			p.loc, p.tzSource = loc, "schedule"
		}
	}
	n := defaultCoachMonths
	if v := strings.TrimSpace(r.URL.Query().Get("months")); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 || parsed > maxSavingsMonths {
			http.Error(w, "months must be 1-12", http.StatusBadRequest)
			return
		}
		n = parsed
	}
	cur := a.now().In(p.loc)
	endMonth := time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, p.loc)
	var periods []ledger.Period
	for i := n - 1; i >= 0; i-- {
		pp, err := ledger.ParsePeriod(endMonth.AddDate(0, -i, 0).Format("2006-01"), p.loc)
		if err != nil {
			http.Error(w, "months must be 1-12", http.StatusBadRequest)
			return
		}
		periods = append(periods, pp)
	}
	// A free account's window is the current month and the twelve before; months older than it are left out.
	if from, on := moneyWindowStart(r.Context(), a.now(), p.loc); on {
		var keep []ledger.Period
		for _, pp := range periods {
			if !pp.Start.Before(from) {
				keep = append(keep, pp)
			}
		}
		if len(keep) == 0 {
			writeOutsideFreeWindow(w, r)
			return
		}
		periods = keep
	}
	in, err := a.input(r.Context(), uid, p, periods...)
	if err != nil {
		http.Error(w, "ledger: "+err.Error(), http.StatusInternalServerError)
		return
	}
	flat := a.flatRate != nil && a.flatRate(r.Context(), uid)
	writeJSON(w, http.StatusOK, ledger.BuildCoach(in, periods, sch, flat))
}
