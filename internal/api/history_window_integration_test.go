//go:build integration

package api

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/statement"
)

// The free plan's history window against real rows. A free and a Pro account hold the same data: a drive and a charge 400 days old and
// a drive and a charge from the last days. Through the real handlers behind the real middleware, the free account sees only the recent
// rows, a single old row answers 402 outside_free_window, a client range is clamped, and every number derived from the whole history is
// the number the Pro account sees; the full export and the backup of the free account still contain the old rows.
func TestHistoryWindowAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "win-free")
	pro, _ := db.EnsureUser(ctx, pool, "win-pro")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	cf, df, sf := charges.NewFactory(pool, resolvers), drives.NewFactory(pool, resolvers), settings.NewFactory(pool)
	now := time.Now().UTC().Truncate(time.Second)
	old, recent := now.AddDate(0, 0, -380), now.AddDate(0, 0, -5) // old: well outside 90 days, inside the 400-day range a chart may ask for
	for _, u := range []uuid.UUID{free, pro} {
		// The only charge before the recent drive is the old one, priced: the recent drive's cost estimate is derived from it.
		if err := cf.For(u).Upsert(ctx, charges.Charge{ID: "win-old-charge", VehicleID: "rv-win", StartedAt: old, EndedAt: old.Add(time.Hour),
			EnergyAddedKWh: 40, Cost: 8, Currency: "USD", PricePerKWh: 0.2, MaxPowerKW: 11, Lat: 30.5, Lon: -97.5, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
		if err := cf.For(u).Upsert(ctx, charges.Charge{ID: "win-new-charge", VehicleID: "rv-win", StartedAt: recent.Add(4 * time.Hour), EndedAt: recent.Add(5 * time.Hour),
			EnergyAddedKWh: 20, MaxPowerKW: 11, Lat: 30.5, Lon: -97.5, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
		for id, at := range map[string]time.Time{"win-old-drive": old.Add(2 * time.Hour), "win-new-drive": recent} {
			if err := df.For(u).Upsert(ctx, drives.Drive{ID: id, VehicleID: "rv-win", StartedAt: at, EndedAt: at.Add(30 * time.Minute), StartSoCPct: 80, EndSoCPct: 70,
				DistanceMi: 12, EnergyUsedKWh: 5, Source: "live"}); err != nil {
				t.Fatal(err)
			}
		}
	}

	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	win := historyWindowMiddleware(ent)
	rt := chi.NewRouter()
	rt.Use(func(next http.Handler) http.Handler { // the signed-in user, from a header
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := uuid.Parse(r.Header.Get("X-Test-User"))
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	})
	userHandler := func(f func(uid uuid.UUID) http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			uid, _ := auth.UserFromContext(r.Context())
			f(uid)(w, r)
		}
	}
	rt.With(win).Get("/drives", userHandler(func(u uuid.UUID) http.HandlerFunc {
		return handleDrives(df.For(u), cf.For(u), sf.For(u))
	}))
	rt.With(win).Get("/drives/{id}", userHandler(func(u uuid.UUID) http.HandlerFunc {
		return handleDriveGet(df.For(u), cf.For(u), sf.For(u))
	}))
	rt.With(win).Get("/charges", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleCharges(cf.For(u), sf.For(u), nil) }))
	rt.With(win).Get("/charges/{id}", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleGetCharge(cf.For(u), sf.For(u), nil) }))
	rt.With(win).Get("/charges/clusters", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleChargeClusters(cf.For(u)) }))
	rt.With(win).Get("/activity", userHandler(func(u uuid.UUID) http.HandlerFunc {
		return handleActivity(df.For(u), cf.For(u), sf.For(u), nil)
	}))
	rt.With(win).Get("/chart-points", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleChartPoints(df.For(u), cf.For(u)) }))
	rt.With(win).Get("/drives/{id}/weather", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleDriveWeatherGet(pool, u) }))

	get := func(u uuid.UUID, path string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Test-User", u.String())
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	ids := func(body []byte, key string) []string {
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		var out []string
		for _, r := range rows {
			if v, ok := r[key].(string); ok {
				out = append(out, v)
			}
		}
		return out
	}
	has := func(list []string, id string) bool {
		for _, x := range list {
			if x == id {
				return true
			}
		}
		return false
	}

	// Lists: the free account sees the recent rows only; the Pro account sees both.
	for _, c := range []struct{ path, key, oldID, newID string }{
		{"/drives", "ID", "win-old-drive", "win-new-drive"},
		{"/charges", "ID", "win-old-charge", "win-new-charge"},
	} {
		code, body := get(free, c.path)
		if got := ids(body, c.key); code != 200 || has(got, c.oldID) || !has(got, c.newID) {
			t.Errorf("free %s: %d %v, want only the recent row", c.path, code, got)
		}
		code, body = get(pro, c.path)
		if got := ids(body, c.key); code != 200 || !has(got, c.oldID) || !has(got, c.newID) {
			t.Errorf("pro %s: %d %v, want both rows", c.path, code, got)
		}
	}

	// A single old row: 402 outside_free_window for the free account, served to Pro; the recent one is served to both.
	for _, p := range []string{"/drives/win-old-drive", "/charges/win-old-charge", "/drives/win-old-drive/weather"} {
		code, body := get(free, p)
		var pb paywallBody
		_ = json.Unmarshal(body, &pb)
		if code != http.StatusPaymentRequired || pb.Error != "outside_free_window" || pb.FreeDays != 90 {
			t.Errorf("free %s: %d %s, want 402 outside_free_window with free_days 90", p, code, body)
		}
	}
	if code, _ := get(pro, "/drives/win-old-drive"); code != 200 {
		t.Errorf("pro old drive: %d", code)
	}
	if code, _ := get(pro, "/charges/win-old-charge"); code != 200 {
		t.Errorf("pro old charge: %d", code)
	}
	for _, p := range []string{"/drives/win-new-drive", "/charges/win-new-charge"} {
		if code, _ := get(free, p); code != 200 {
			t.Errorf("free %s: %d, want 200", p, code)
		}
	}
	if code, _ := get(free, "/drives/does-not-exist"); code != http.StatusNotFound {
		t.Errorf("a missing drive: %d, want the handler's own 404", code)
	}

	// Derived values are computed over everything: the recent drive's cost estimate comes from the OLD priced charge, and is the same for a
	// free and a Pro account.
	costOf := func(u uuid.UUID) (float64, float64) {
		_, body := get(u, "/drives/win-new-drive")
		var d struct {
			Cost float64 `json:"estimated_cost"`
			Rate float64 `json:"estimated_price_per_kwh"`
		}
		_ = json.Unmarshal(body, &d)
		return d.Cost, d.Rate
	}
	fc, fr := costOf(free)
	pc, pr := costOf(pro)
	if fc == 0 || fc != pc || fr != pr {
		t.Errorf("derived cost: free %v at %v, pro %v at %v; want equal and non-zero (rate from the old charge)", fc, fr, pc, pr)
	}

	// Client ranges are clamped: asking for the last 400 days returns the same rows as the window for a free account.
	since := url_(now.AddDate(0, 0, -399)) // as far back as a chart may ask, which reaches the old rows
	code, body := get(free, "/chart-points?since="+since+"&until="+url_(now.Add(time.Hour)))
	if code != http.StatusBadRequest && code != 200 {
		t.Fatalf("chart-points: %d %s", code, body)
	}
	if code == 200 {
		var cp struct {
			Drives []map[string]any `json:"drives"`
		}
		_ = json.Unmarshal(body, &cp)
		for _, d := range cp.Drives {
			if s, _ := d["started_at"].(string); s != "" {
				if ts, err := time.Parse(time.RFC3339, s); err == nil && ts.Before(now.AddDate(0, 0, -91)) {
					t.Errorf("chart-points returned a drive from %v to a free account", ts)
				}
			}
		}
	}
	code, body = get(free, "/activity?since="+since+"&until="+url_(now.Add(time.Hour)))
	if code == 200 && (strings.Contains(string(body), "win-old-drive") || strings.Contains(string(body), "win-old-charge")) {
		t.Errorf("activity gave a free account rows older than the window: %s", body)
	}
	if code, body = get(pro, "/activity?since="+since+"&until="+url_(now.Add(time.Hour))); code != 200 && code != http.StatusBadRequest {
		t.Errorf("pro activity: %d %s", code, body)
	}

	// Clusters are formed over every charge and only the members inside the window are returned.
	_, freeClusters := get(free, "/charges/clusters")
	_, proClusters := get(pro, "/charges/clusters")
	if strings.Contains(string(freeClusters), "win-old-charge") {
		t.Errorf("clusters gave a free account a charge older than the window: %s", freeClusters)
	}
	if string(proClusters) != "[]\n" && !strings.Contains(string(proClusters), "win-old-charge") {
		t.Errorf("a Pro account's clusters lack the old charge: %s", proClusters)
	}

	// The export and the backup of the free account are complete: the old rows are in them. (They are not behind the window.)
	d := poolOn(t, Deps{DB: pool, Charges: cf, Drives: df, Settings: sf, Version: "test", Samples: nil})
	req := httptest.NewRequest(http.MethodGet, "/api/export", nil).WithContext(auth.WithUser(ctx, free))
	rec := httptest.NewRecorder()
	handleExport(d)(rec, req)
	if rec.Code != 200 {
		t.Fatalf("export: %d", rec.Code)
	}
	files := readZip(t, rec.Body.Bytes())
	if !strings.Contains(files["drives.csv"], "win-old-drive") || !strings.Contains(files["charges.csv"], "win-old-charge") {
		t.Errorf("the free account's export is missing rows older than 90 days:\ndrives.csv: %s\ncharges.csv: %s", files["drives.csv"], files["charges.csv"])
	}
	// The backup is gzip by default (application/gzip, not a content encoding): read it as the download is read.
	brec := httptest.NewRecorder()
	handleDataBackup(backupTestDepsFor(t, pool))(brec, httptest.NewRequest(http.MethodGet, "/api/data/backup", nil).WithContext(auth.WithUser(ctx, free)))
	if brec.Code != 200 || brec.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("backup: %d %s", brec.Code, brec.Header().Get("Content-Type"))
	}
	zr, err := gzip.NewReader(brec.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "win-old-drive") || !strings.Contains(string(raw), "win-old-charge") {
		t.Errorf("the free account's backup is missing rows older than 90 days (%d bytes)", len(raw))
	}
}

func url_(t time.Time) string { return strings.ReplaceAll(t.UTC().Format(time.RFC3339), "+", "%2B") }

// Money under the free window: a free account's statements are the current month and the two before; an older month is 402
// outside_free_window; the statement CSV is the current month only (any other month is a Pro convenience: 402 pro_required); the summary
// list and the savings months stop at the window; the open-items count matches the windowed to-confirm list. A Pro account sees it all.
func TestMoneyWindowAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "money-free")
	pro, _ := db.EnsureUser(ctx, pool, "money-pro")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	cf, sf := charges.NewFactory(pool, resolvers), settings.NewFactory(pool)
	now := time.Now().UTC()
	month := func(offset int) time.Time {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, offset, 0)
	}
	// One priced charge in the current month, the previous month and four months ago; the current and the old one are open for verification.
	for _, u := range []uuid.UUID{free, pro} {
		for i, off := range []int{0, -1, -2, -3, -4} {
			c := charges.Charge{ID: "money-charge-" + month(off).Format("2006-01"), VehicleID: "rv-money", StartedAt: month(off).Add(time.Minute), EndedAt: month(off).Add(41 * time.Minute),
				EnergyAddedKWh: 20, Cost: 5, Currency: "USD", PricePerKWh: 0.25, MaxPowerKW: 11, FinalState: "charging_complete", Source: "live"}
			if i == 0 || i == 4 {
				c.NetworkState = "estimated"
			}
			if err := cf.For(u).Upsert(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}

	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	win := historyWindowMiddleware(ent)
	la := newLedgerAPI(pool, sf)
	rt := chi.NewRouter()
	rt.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := uuid.Parse(r.Header.Get("X-Test-User"))
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	})
	withUser := func(f func(uuid.UUID, http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, _ := auth.UserFromContext(r.Context())
			f(u, w, r)
		}
	}
	rt.With(win).Get("/ledger/monthly", withUser(la.monthly))
	rt.With(win).Get("/ledger/monthly.csv", withUser(la.statementCSV))
	rt.With(win).Get("/ledger/summary", withUser(la.summary))
	rt.With(win).Get("/ledger/trips", withUser(la.trips))
	rt.With(win).Get("/ledger/savings", withUser(la.savings))
	stm := newStatementAPI(pool, Deps{Settings: sf}, statement.NewSigner([]byte("0123456789abcdef0123456789abcdef")))
	stm.flag = func(string) bool { return true }
	rt.With(win).Get("/statement-email/preview", withUser(stm.preview))
	get := func(u uuid.UUID, path string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Test-User", u.String())
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	errCode := func(body []byte) string {
		var pb paywallBody
		_ = json.Unmarshal(body, &pb)
		return pb.Error
	}
	cur, prev, prev2, prev3, old := month(0).Format("2006-01"), month(-1).Format("2006-01"), month(-2).Format("2006-01"), month(-3).Format("2006-01"), month(-4).Format("2006-01")

	// Statements.
	for _, m := range []string{cur, prev} {
		if code, body := get(free, "/ledger/monthly?month="+m); code != 200 {
			t.Errorf("free %s: %d %s, want 200", m, code, body)
		}
	}
	if code, body := get(free, "/ledger/monthly?month="+old); code != http.StatusPaymentRequired || errCode(body) != "outside_free_window" {
		t.Errorf("free %s: %d %s, want 402 outside_free_window", old, code, body)
	}
	for _, m := range []string{cur, prev, old} {
		if code, body := get(pro, "/ledger/monthly?month="+m); code != 200 {
			t.Errorf("pro %s: %d %s", m, code, body)
		}
	}
	// The month's total is the same number for a free and a Pro account.
	spendOf := func(u uuid.UUID) string {
		_, body := get(u, "/ledger/monthly?month="+prev)
		var m struct {
			Spend  json.RawMessage `json:"spend"`
			Energy json.RawMessage `json:"energy"`
		}
		_ = json.Unmarshal(body, &m)
		return string(m.Spend) + string(m.Energy)
	}
	if f, p := spendOf(free), spendOf(pro); f == "" || f != p {
		t.Errorf("a month inside the window differs between free and Pro:\nfree %s\npro  %s", f, p)
	}

	// The statement CSV: any month the free statement shows (the current month and the two before) is downloadable; older months are Pro.
	for _, m := range []string{cur, prev, prev2} {
		if code, body := get(free, "/ledger/monthly.csv?month="+m); code != 200 {
			t.Errorf("free CSV for %s: %d %s, want 200 (the statement shows it)", m, code, body)
		}
	}
	for _, m := range []string{prev3, old} {
		if code, body := get(free, "/ledger/monthly.csv?month="+m); code != http.StatusPaymentRequired || errCode(body) != "pro_required" {
			t.Errorf("free CSV for %s: %d %s, want 402 pro_required", m, code, body)
		}
		if code, _ := get(pro, "/ledger/monthly.csv?month="+m); code != 200 {
			t.Errorf("pro CSV for %s: %d", m, code)
		}
	}

	// Summary: the list stops at the window; the open-items count matches the windowed list (the old open charge is not counted).
	summary := func(u uuid.UUID) (months int, open int) {
		code, body := get(u, "/ledger/summary?months=12")
		var s struct {
			Months         []json.RawMessage `json:"months"`
			ToConfirmCount int               `json:"to_confirm_count"`
		}
		if code != 200 || json.Unmarshal(body, &s) != nil {
			t.Fatalf("summary: %d %s", code, body)
		}
		return len(s.Months), s.ToConfirmCount
	}
	if m, open := summary(free); m != 3 || open != 1 {
		t.Errorf("free summary: %d months and %d open, want 3 and 1", m, open)
	}
	if m, open := summary(pro); m != 12 || open != 2 {
		t.Errorf("pro summary: %d months and %d open, want 12 and 2", m, open)
	}

	// Savings: the months stop at the window; asking only for an old month is 402. Trips: a wide range is accepted and clamped.
	savingsMonths := func(u uuid.UUID, q string) (int, []byte, int) {
		code, body := get(u, "/ledger/savings?"+q)
		var s struct {
			Months []string `json:"months"`
		}
		_ = json.Unmarshal(body, &s)
		return code, body, len(s.Months)
	}
	if code, body, n := savingsMonths(free, "month="+cur+"&months=12"); code != 200 || n != 3 {
		t.Errorf("free savings over 12 months: %d, %d months (%s), want 200 and 3", code, n, body)
	}
	if code, _, n := savingsMonths(pro, "month="+cur+"&months=12"); code != 200 || n != 12 {
		t.Errorf("pro savings over 12 months: %d, %d months, want 200 and 12", code, n)
	}
	if code, body, _ := savingsMonths(free, "month="+old+"&months=1"); code != http.StatusPaymentRequired || errCode(body) != "outside_free_window" {
		t.Errorf("free savings for %s: %d %s, want 402 outside_free_window", old, code, body)
	}
	wide := "/ledger/trips?from=" + now.AddDate(0, 0, -300).Format("2006-01-02") + "&to=" + now.Format("2006-01-02")
	for _, u := range []uuid.UUID{free, pro} {
		if code, body := get(u, wide); code != 200 {
			t.Errorf("trips over 300 days: %d %s", code, body)
		}
	}
	// The statement preview follows the same months; the email job itself is not windowed.
	for _, m := range []string{cur, prev} {
		if code, body := get(free, "/statement-email/preview?month="+m); code != 200 {
			t.Errorf("free preview %s: %d %s", m, code, body)
		}
	}
	if code, body := get(free, "/statement-email/preview?month="+old); code != http.StatusPaymentRequired || errCode(body) != "outside_free_window" {
		t.Errorf("free preview %s: %d %s, want 402 outside_free_window", old, code, body)
	}
	if code, body := get(pro, "/statement-email/preview?month="+old); code != 200 {
		t.Errorf("pro preview %s: %d %s", old, code, body)
	}
	// The comparison with the month before: for the OLDEST month a free account may preview (two months back) the month before it is outside
	// the window, so the "than <month>" line is left out; a Pro account sees it. (Positive control: the Pro text names that month.)
	previewText := func(u uuid.UUID) string {
		code, body := get(u, "/statement-email/preview?month="+prev2)
		var pv struct {
			Text string `json:"text"`
		}
		if code != 200 || json.Unmarshal(body, &pv) != nil {
			t.Fatalf("preview %s: %d %s", prev2, code, body)
		}
		return pv.Text
	}
	compared := month(-3).Format("January") // the month before prev2, as the delta line names it
	if pt := previewText(pro); !strings.Contains(pt, compared) {
		t.Errorf("the Pro preview of %s does not compare with %s (no positive control): %s", prev2, compared, pt)
	}
	if ft := previewText(free); strings.Contains(ft, compared) {
		t.Errorf("the free preview of %s still compares with %s, which is outside the window: %s", prev2, compared, ft)
	}

	// A range wholly before a free account's window is empty, not an error.
	before := "/ledger/trips?from=" + now.AddDate(0, 0, -300).Format("2006-01-02") + "&to=" + now.AddDate(0, 0, -200).Format("2006-01-02")
	if code, body := get(free, before); code != 200 {
		t.Errorf("trips wholly before the window: %d %s", code, body)
	}
}

// The analytics reads under the free window (part 3): raw samples, the sleep series and the efficiency insights are clamped to the window
// (a free account asking for a year gets the last 90 days; Pro gets what it asks for); trends, range near a temperature and the capacity
// estimate are Pro-only (402 pro_required on a free account).
func TestAnalyticsWindowAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "ana-free")
	pro, _ := db.EnsureUser(ctx, pool, "ana-pro")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	df := drives.NewFactory(pool, resolvers)
	smf := samples.NewFactory(pool, resolvers)
	now := time.Now().UTC().Truncate(time.Second)
	day := func(daysAgo int) time.Time {
		d := now.AddDate(0, 0, -daysAgo)
		return time.Date(d.Year(), d.Month(), d.Day(), 10, 0, 0, 0, time.UTC)
	}
	old, recent := day(300), day(5)
	for _, u := range []uuid.UUID{free, pro} {
		for _, at := range []time.Time{old, recent} {
			if err := df.For(u).Upsert(ctx, drives.Drive{ID: "ana-drive-" + at.Format("0102"), VehicleID: "rv-ana", StartedAt: at, EndedAt: at.Add(30 * time.Minute),
				StartSoCPct: 80, EndSoCPct: 70, DistanceMi: 12, EnergyUsedKWh: 5, AvgSpeedMph: 40, Source: "live"}); err != nil {
				t.Fatal(err)
			}
		}
		var vid uuid.UUID
		if err := pool.QueryRowContext(ctx, `SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = 'rv-ana'`, u).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		// Two parked samples an hour apart on each day, the car asleep.
		for _, at := range []time.Time{old, recent} {
			if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, at); err != nil {
				t.Fatal(err)
			}
			for _, h := range []int{0, 1} {
				if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (vehicle_id, user_id, at, source, battery_level_pct, power_state, shift_state, charging_state)
					VALUES ($1, $2, $3, 'live', 70, 'sleep', 'P', 'not_charging')`, vid, u, at.Add(time.Duration(h)*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	win := historyWindowMiddleware(ent)
	proOnly := proOnlyMiddleware(ent)
	rt := chi.NewRouter()
	rt.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := uuid.Parse(r.Header.Get("X-Test-User"))
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	})
	userHandler := func(f func(uid uuid.UUID) http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			uid, _ := auth.UserFromContext(r.Context())
			f(uid)(w, r)
		}
	}
	rt.With(win).Get("/samples", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleSamples(smf.For(u)) }))
	rt.With(win).Get("/sleep-activity", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleSleepActivity(u, smf.For(u)) }))
	rt.With(win).Get("/insights/efficiency", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleEfficiencyInsights(smf.For(u), nil) }))
	rt.With(proOnly).Get("/vehicles/{vehicleID}/trends", userHandler(func(u uuid.UUID) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { handleVehicleTrends(pool)(u, w, r) }
	}))
	rt.With(proOnly).Get("/vehicles/{vehicleID}/cold-range", userHandler(func(u uuid.UUID) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { handleColdRange(pool)(u, w, r) }
	}))
	rt.With(proOnly).Get("/vehicles/{vehicleID}/pack-health", userHandler(func(u uuid.UUID) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { handlePackHealthGet(pool, nil)(u, w, r) }
	}))
	get := func(u uuid.UUID, path string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Test-User", u.String())
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	ts := func(t time.Time) string { return url_(t) }
	since := ts(now.AddDate(0, 0, -350))
	until := ts(now.Add(time.Hour))

	// Raw samples: the free account's year-long range is clamped to the window (the old samples are not returned); Pro gets all four.
	count := func(u uuid.UUID, path string) int {
		code, body := get(u, path)
		var rows []json.RawMessage
		if code != 200 || json.Unmarshal(body, &rows) != nil {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		return len(rows)
	}
	if n := count(free, "/samples?since="+since+"&until="+until); n != 2 {
		t.Errorf("free samples over 350 days: %d rows, want the 2 inside the window", n)
	}
	if n := count(pro, "/samples?since="+since+"&until="+until); n != 4 {
		t.Errorf("pro samples over 350 days: %d rows, want 4", n)
	}

	// The sleep series: one day inside the window for free, both for Pro. (The cache key carries the clamped range, so one plan can't be
	// served the other's series.)
	if n := count(free, "/sleep-activity?since="+since+"&until="+until); n != 1 {
		t.Errorf("free sleep-activity: %d days, want 1", n)
	}
	if n := count(pro, "/sleep-activity?since="+since+"&until="+until); n != 2 {
		t.Errorf("pro sleep-activity: %d days, want 2", n)
	}
	if n := count(free, "/sleep-activity?since="+since+"&until="+until); n != 1 {
		t.Errorf("free sleep-activity after a Pro request for the same range: %d days, want 1 (a cached Pro series was served)", n)
	}

	// Efficiency insights: a free account asking for a year gets exactly the answer for the window; Pro's covers the old drive too.
	body := func(u uuid.UUID, path string) string {
		code, b := get(u, path)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, b)
		}
		return string(b)
	}
	freeYear := body(free, "/insights/efficiency?since="+since+"&until="+until)
	freeWindow := body(free, "/insights/efficiency?since="+ts(now.AddDate(0, 0, -80))+"&until="+until)
	proYear := body(pro, "/insights/efficiency?since="+since+"&until="+until)
	if freeYear != freeWindow {
		t.Errorf("a free account's year of insights differs from its window:\nyear   %s\nwindow %s", freeYear, freeWindow)
	}
	if freeYear == proYear {
		t.Errorf("a free and a Pro account got the same year of insights: the old drive was not left out for free")
	}

	// Pro-only reads.
	for _, p := range []string{"/vehicles/rv-ana/trends", "/vehicles/rv-ana/cold-range", "/vehicles/rv-ana/pack-health"} {
		code, b := get(free, p)
		var pb paywallBody
		_ = json.Unmarshal(b, &pb)
		if code != http.StatusPaymentRequired || pb.Error != "pro_required" {
			t.Errorf("free %s: %d %s, want 402 pro_required", p, code, b)
		}
		if code, b := get(pro, p); code == http.StatusPaymentRequired {
			t.Errorf("pro %s: %d %s, a Pro account was refused", p, code, b)
		}
	}
}

// The free account's delete-by-range (the privacy exit for rows outside its window): drives, charges and archived months before the cutoff
// month go; the cutoff month on, and every other account's rows, stay. Free, no Pro needed; a malformed or future cutoff and a missing
// confirm are refused.
func TestDeleteRangeAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "range-free")
	other, _ := db.EnsureUser(ctx, pool, "range-other")
	resolvers := db.NewVehicleResolverFactory(pool)
	cf, df, sf := charges.NewFactory(pool, resolvers), drives.NewFactory(pool, resolvers), samples.NewFactory(pool, resolvers)
	now := time.Now().UTC().Truncate(time.Second)
	old, recent := now.AddDate(0, 0, -380), now.AddDate(0, 0, -5)
	cutoffMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -6, 0)
	vid := uuid.New()
	for _, u := range []uuid.UUID{free, other} {
		for id, at := range map[string]time.Time{"rg-old-charge": old, "rg-new-charge": recent} {
			if err := cf.For(u).Upsert(ctx, charges.Charge{ID: id, VehicleID: "rv-range", StartedAt: at, EndedAt: at.Add(time.Hour), EnergyAddedKWh: 10,
				MaxPowerKW: 11, FinalState: "charging_complete", Source: "live"}); err != nil {
				t.Fatal(err)
			}
		}
		for id, at := range map[string]time.Time{"rg-old-drive": old, "rg-new-drive": recent} {
			if err := df.For(u).Upsert(ctx, drives.Drive{ID: id, VehicleID: "rv-range", StartedAt: at, EndedAt: at.Add(30 * time.Minute), DistanceMi: 5, Source: "live"}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO drive_weather (user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider, temp_c)
			VALUES ($1, 'rg-old-drive', 30.5, -97.5, $2, 'test', 10)`, u, old); err != nil {
			t.Fatal(err)
		}
		for id, at := range map[string]time.Time{"rg-old": old, "rg-new": recent} {
			if _, err := pool.ExecContext(ctx, `INSERT INTO rivian_receipts (user_id, transaction_id, started_at, ended_at, city) VALUES ($1, $2, $3, $3, 'Testville')`, u, id, at); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.ExecContext(ctx, `INSERT INTO push_notifications (id, user_id, kind, title, body, created_at) VALUES ($1, $2, 'x', 't', 'b', $3)`, uuid.New(), u, at); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.ExecContext(ctx, `INSERT INTO charges_deleted_backup (batch_id, kind, charge_id, user_id, row) VALUES ($1, 'charge', $2, $3, jsonb_build_object('started_at', $4::timestamptz))`,
				uuid.New(), uuid.New(), u, at); err != nil {
				t.Fatal(err)
			}
		}
		for _, m := range []time.Time{old, recent} {
			month := time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)
			if _, err := pool.ExecContext(ctx, `INSERT INTO archive_months (user_id, vehicle_id, month, rows, first_at, last_at, sha256_object, bytes)
				VALUES ($1, $2, $3::date, 1, $3::timestamptz, $3::timestamptz, 'x', 1) ON CONFLICT DO NOTHING`, u, vid, month); err != nil {
				t.Fatal(err)
			}
		}
	}

	h := handleDataDeleteRange(Deps{Drives: df, Charges: cf, Samples: sf, DB: pool})
	post := func(u uuid.UUID, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/data/delete-range", strings.NewReader(body))
		req = req.WithContext(auth.WithUser(req.Context(), u))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	cut := cutoffMonth.Format("2006-01")
	for _, body := range []string{`{"before":"` + cut + `"}`, `{"before":"nope","confirm":true}`, `{"before":"2999-01","confirm":true}`, `{}`} {
		if code := post(free, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, code)
		}
	}
	if n := countRows(ctx, t, pool, `SELECT count(*) FROM charges WHERE user_id = $1`, free); n != 2 {
		t.Fatalf("refused requests deleted rows: %d charges left", n)
	}
	if code := post(free, `{"before":"`+cut+`","confirm":true}`); code != http.StatusOK {
		t.Fatalf("delete-range: %d", code)
	}
	for _, c := range []struct {
		table, id string
		wantFree  int
	}{{"charges", "rg-old-charge", 0}, {"charges", "rg-new-charge", 1}, {"drives", "rg-old-drive", 0}, {"drives", "rg-new-drive", 1}} {
		q := `SELECT count(*) FROM ` + c.table + ` WHERE user_id = $1 AND external_id = '` + c.id + `'`
		if n := countRows(ctx, t, pool, q, free); n != c.wantFree {
			t.Errorf("free %s %s: %d rows, want %d", c.table, c.id, n, c.wantFree)
		}
		if n := countRows(ctx, t, pool, q, other); n != 1 {
			t.Errorf("another account's %s %s: %d rows, want 1 (untouched)", c.table, c.id, n)
		}
	}
	if n := countRows(ctx, t, pool, `SELECT count(*) FROM drive_weather WHERE user_id = $1 AND drive_id = 'rg-old-drive'`, free); n != 0 {
		t.Errorf("per-drive weather of a deleted drive remains: %d", n)
	}
	for _, tbl := range []string{"rivian_receipts", "push_notifications", "charges_deleted_backup"} {
		if n := countRows(ctx, t, pool, `SELECT count(*) FROM `+tbl+` WHERE user_id = $1`, free); n != 1 {
			t.Errorf("free %s: %d rows, want only the recent one", tbl, n)
		}
		if n := countRows(ctx, t, pool, `SELECT count(*) FROM `+tbl+` WHERE user_id = $1`, other); n != 2 {
			t.Errorf("another account's %s: %d rows, want 2", tbl, n)
		}
	}
	oldMonth := time.Date(old.Year(), old.Month(), 1, 0, 0, 0, 0, time.UTC)
	if n := countRows(ctx, t, pool, `SELECT count(*) FROM archive_months WHERE user_id = $1 AND month = $2`, free, oldMonth); n != 0 {
		t.Errorf("archive_months of the old month remains: %d", n)
	}
	if n := countRows(ctx, t, pool, `SELECT count(*) FROM archive_months WHERE user_id = $1`, free); n != 1 {
		t.Errorf("free archive_months: %d, want only the recent month", n)
	}
	if n := countRows(ctx, t, pool, `SELECT count(*) FROM archive_months WHERE user_id = $1`, other); n != 2 {
		t.Errorf("another account's archive_months: %d, want 2", n)
	}
	wantPrefix := "v1/u=" + free.String() + "/v=" + vid.String() + "/" + oldMonth.Format("2006-01") + "."
	if n := countRows(ctx, t, pool, `SELECT count(*) FROM archive_deletions WHERE user_id = $1 AND prefix = $2`, free, wantPrefix); n != 1 {
		t.Errorf("archive deletion queued for the old month: %d, want 1", n)
	}
	if n := countRows(ctx, t, pool, `SELECT count(*) FROM archive_deletions WHERE user_id = $1`, free); n != 1 {
		t.Errorf("archive deletions queued: %d, want exactly the old month", n)
	}
	if n := countRows(ctx, t, pool, `SELECT count(*) FROM archive_deletions WHERE user_id = $1`, other); n != 0 {
		t.Errorf("archive deletions queued for another account: %d", n)
	}
	if err := db.DeleteUserArchiveBefore(ctx, pool, free, cutoffMonth.Add(time.Hour)); err == nil {
		t.Error("a cutoff that is not the start of a month was accepted")
	}
}

func countRows(ctx context.Context, t *testing.T, pool interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, q string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}
