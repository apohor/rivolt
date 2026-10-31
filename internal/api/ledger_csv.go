package api

import (
	"encoding/csv"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
)

// statementHeader is the one header of the month CSV. Every row has the same
// columns; `record` says what a row is, so the file stays a plain table a
// spreadsheet can filter and sum.
var statementHeader = []string{
	"record", "month", "currency", "key_1", "key_2", "sessions", "kwh", "cost",
	"started_at", "ended_at", "place", "kind", "cost_source", "network", "flags",
}

// statementCSV serves GET /api/ledger/monthly.csv?month=YYYY-MM[&tz][&vehicle_id]:
// the month's statement as a CSV download, from the same numbers as
// GET /api/ledger/monthly (it is built from that response's own structure, so the
// totals match the Money tab). Rows:
//
//	total        key_1 spend (the month's counted cost) and unpriced (energy with no price)
//	by_source    key_1 the cost source (rivian, home_rate, network_estimate, ...), cost
//	by_place_kind key_1 place (home, away, unknown), key_2 kind (ac, dcfc), sessions, kwh, cost
//	session      one line per charging session: local start and end, place, kind, cost
//	             source, the network's display name, kwh, cost (blank when unpriced)
//
// Own data only (the request's user); text cells that start with = + - @ are
// escaped so a spreadsheet cannot run them.
func (a ledgerAPI) statementCSV(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
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
	// A free account downloads the CSV of any month its statement shows (the current month and the two before), so nothing it can see is
	// behind a wall; an older month is a Pro convenience (the full export, which has everything, is always free).
	if from, on := moneyWindowStart(r.Context(), a.now(), p.loc); on && period.Start.Before(from) {
		writeProRequired(w)
		return
	}
	in, err := a.input(r.Context(), uid, p, period)
	if err != nil {
		http.Error(w, "ledger: "+err.Error(), http.StatusInternalServerError)
		return
	}
	m := ledger.Build(in, period, true)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="rivolt-statement-`+period.Month+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	cw := csv.NewWriter(w)
	row := func(cells ...string) { _ = cw.Write(cells) }
	row(statementHeader...)
	cur := csvText(m.Currency)
	base := func(record string) []string {
		c := make([]string, len(statementHeader))
		c[0], c[1], c[2] = record, period.Month, cur
		return c
	}
	set := func(c []string, col string, v string) {
		for i, h := range statementHeader {
			if h == col {
				c[i] = v
			}
		}
	}

	t := base("total")
	set(t, "key_1", "spend")
	set(t, "sessions", strconv.Itoa(m.Spend.PricedSessions))
	set(t, "kwh", fnum(m.Energy.KWhAdded))
	set(t, "cost", fnum(m.Spend.Total))
	row(t...)
	u := base("total")
	set(u, "key_1", "unpriced")
	set(u, "sessions", strconv.Itoa(m.Spend.UnpricedSessions))
	set(u, "kwh", fnum(m.Spend.UnpricedKWh))
	row(u...)

	sources := make([]string, 0, len(m.Spend.BySource))
	for k := range m.Spend.BySource {
		sources = append(sources, k)
	}
	sort.Strings(sources)
	for _, k := range sources {
		c := base("by_source")
		set(c, "key_1", csvText(k))
		set(c, "cost", fnum(m.Spend.BySource[k]))
		row(c...)
	}
	for _, place := range []string{"home", "away", "unknown"} {
		for _, kind := range []string{"ac", "dcfc"} {
			l := m.Energy.ByPlaceKind[place][kind]
			c := base("by_place_kind")
			set(c, "key_1", place)
			set(c, "key_2", kind)
			set(c, "sessions", strconv.Itoa(l.Sessions))
			set(c, "kwh", fnum(l.KWh))
			set(c, "cost", fnum(l.Cost))
			row(c...)
		}
	}
	for _, rc := range m.Receipts {
		for _, s := range rc.Sessions {
			c := base("session")
			set(c, "sessions", "1")
			if s.KWh != nil {
				set(c, "kwh", fnum(*s.KWh))
			}
			if s.Cost != nil && s.CostSource != ledger.SourceUnknown {
				set(c, "cost", fnum(*s.Cost))
			}
			set(c, "started_at", s.StartedAt.In(p.loc).Format("2006-01-02T15:04:05Z07:00"))
			set(c, "ended_at", s.EndedAt.In(p.loc).Format("2006-01-02T15:04:05Z07:00"))
			set(c, "place", csvText(s.Place))
			set(c, "kind", csvText(s.Kind))
			set(c, "cost_source", csvText(s.CostSource))
			if s.Network != nil {
				set(c, "network", csvText(*s.Network))
			}
			set(c, "flags", csvText(strings.Join(s.Flags, ";")))
			row(c...)
		}
	}
	cw.Flush()
}
