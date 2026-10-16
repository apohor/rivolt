package api

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/receipts"
)

// "Take your data with you" (B11): GET /api/export.
//
// Human- and spreadsheet-friendly, unlike /api/data/backup (a JSON
// dump meant for restore, with raw samples). A zip of CSVs (charges
// with the ledger's effective cost source, drives, trips) plus the
// user's settings and vehicle profiles as JSON and a README, or with
// ?format=json one JSON document with the same sections.
//
// Scoped to the signed-in user, never behind the Money tab (export
// must always work, hosted and self-hosted), and carries no secrets or
// tokens: settings keys that look like credentials are dropped.
// Samples (the raw telemetry) stay out; use /api/data/backup for those.

// exportedSettingKeys is the allowlist of per-user settings that go in
// settings.json. Anything not listed is NOT exported, so a new key (a
// token, an API key, a webhook URL) can't leak by being named
// unexpectedly. TestExportSettingKeysAreClassified fails when a key
// appears in internal/settings without being classified here or in
// notExportedSettingKeys.
var exportedSettingKeys = map[string]bool{
	"home_location.latitude":        true,
	"home_location.longitude":       true,
	"home_location.label":           true,
	"home_location.address":         true,
	"charging.home_price_per_kwh":   true,
	"charging.home_currency":        true,
	"charging.gas_price_per_gallon": true,
	"charging.comparison_mpg":       true,
	"charging.networks":             true,
	"planner.default_drive_mode":    true,
	"planner.has_adapter":           true,
	"planner.favorites":             true,
	"recap.weather_enabled":         true,
}

// notExportedSettingKeys are keys known to exist that are deliberately
// left out: install-wide AI provider configuration (including API
// keys) and GPS thresholds, which are operator settings, not the
// user's data.
var notExportedSettingKeys = map[string]bool{
	"ai.provider": true, "ai.openai.model": true, "ai.openai.api_key": true,
	"ai.anthropic.model": true, "ai.anthropic.api_key": true,
	"ai.gemini.model": true, "ai.gemini.api_key": true,
	"ai.image.provider": true, "ai.image.openai.model": true, "ai.image.gemini.model": true,
	"ai.speech.provider": true, "ai.speech.openai.model": true, "ai.speech.gemini.model": true,
	"gps.threshold_missing_pct": true, "gps.threshold_stale_sec": true, "gps.threshold_jump_count": true,
}

type exportProfile struct {
	RivianVehicleID string            `json:"rivian_vehicle_id"`
	DisplayName     string            `json:"display_name,omitempty"`
	Profile         db.VehicleProfile `json:"profile"`
}

// exportData is everything an export contains.
type exportData struct {
	Version   string
	CreatedAt time.Time
	TZ        string
	Currency  string
	Charges   []charges.Charge
	Classes   []ledger.Classification // parallel to Charges
	Drives    []drives.Drive
	Trips     []ledger.Trip
	Receipts  []receipts.Receipt
	Settings  map[string]string
	Profiles  []exportProfile
}

// exportSettings keeps only allowlisted keys.
func exportSettings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if exportedSettingKeys[k] {
			out[k] = v
		}
	}
	return out
}

// csvText neutralises spreadsheet formula injection in free-text
// cells: a value starting with = + - @ tab or CR gets a leading quote.
func csvText(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func fnum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
func fptr(v *float64) string {
	if v == nil {
		return ""
	}
	return fnum(*v)
}
func tstr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

var exportChargeHeader = []string{
	"id", "vehicle_id", "started_at", "ended_at", "start_soc_pct", "end_soc_pct",
	"energy_added_kwh", "miles_added", "max_power_kw", "avg_power_kw", "active_seconds", "thermal_kwh",
	"final_state", "source", "lat", "lon",
	"cost", "currency", "price_per_kwh", "cost_source", "network", "place",
	"effective_cost_source", "effective_cost_source_derived", "kind", "effective_place", "km_from_home",
	"counts_in_spend", "flags",
	"receipt_cost", "receipt_currency", "receipt_txn_id",
}

var exportReceiptHeader = []string{
	"transaction_id", "started_at", "ended_at", "total_cost", "currency", "energy_kwh", "range_added_km",
	"vendor", "city", "charger_name", "is_home_charger", "matched_to_charge",
}

var exportDriveHeader = []string{
	"id", "vehicle_id", "started_at", "ended_at", "start_soc_pct", "end_soc_pct",
	"start_odometer_mi", "end_odometer_mi", "distance_mi", "energy_used_kwh",
	"max_speed_mph", "avg_speed_mph", "start_lat", "start_lon", "end_lat", "end_lon", "source", "route_polyline",
}

var exportTripHeader = []string{
	"trip_id", "vehicle_id", "started_at", "ended_at", "miles", "fast_sessions", "max_km_from_home",
	"kwh_used", "kwh_added", "cost", "estimated_share", "unpriced_sessions", "efficiency_mi_per_kwh",
	"drive_ids", "charge_ids", "cost_source_mix",
}

func writeChargesCSV(w io.Writer, e exportData) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(exportChargeHeader); err != nil {
		return err
	}
	for i, c := range e.Charges {
		var cl ledger.Classification
		if i < len(e.Classes) {
			cl = e.Classes[i]
		}
		row := []string{
			csvText(c.ID), csvText(c.VehicleID), tstr(c.StartedAt), tstr(c.EndedAt), fnum(c.StartSoCPct), fnum(c.EndSoCPct),
			fnum(c.EnergyAddedKWh), fnum(c.MilesAdded), fnum(c.MaxPowerKW), fnum(c.AvgPowerKW), fnum(c.ActiveSeconds), fptr(c.ThermalKWh),
			csvText(c.FinalState), csvText(c.Source), fnum(c.Lat), fnum(c.Lon),
			fnum(c.Cost), csvText(c.Currency), fnum(c.PricePerKWh), csvText(c.CostSource), csvText(c.Network), csvText(c.Place),
			csvText(cl.CostSource), strconv.FormatBool(cl.CostSourceDerived), csvText(cl.Kind), csvText(cl.Place), fptr(cl.KMFromHome),
			strconv.FormatBool(cl.CountsInSpend), csvText(strings.Join(cl.Flags, ";")),
			fptr(c.ReceiptCost), csvText(c.ReceiptCurrency), csvText(c.ReceiptTxnID),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// writeReceiptsCSV is every Rivian charging receipt we stored (what Rivian
// billed), matched to one of the charges or not.
func writeReceiptsCSV(w io.Writer, e exportData) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(exportReceiptHeader); err != nil {
		return err
	}
	for _, r := range e.Receipts {
		home := ""
		if r.IsHome != nil {
			home = strconv.FormatBool(*r.IsHome)
		}
		row := []string{
			csvText(r.TransactionID), tstr(r.StartedAt), tstr(r.EndedAt), fptr(r.TotalCost), csvText(r.Currency),
			fptr(r.EnergyKWh), fptr(r.RangeAddedKm), csvText(r.Vendor), csvText(r.City), csvText(r.ChargerName),
			home, strconv.FormatBool(r.ChargeID != nil),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func writeDrivesCSV(w io.Writer, e exportData) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(exportDriveHeader); err != nil {
		return err
	}
	for _, d := range e.Drives {
		row := []string{
			csvText(d.ID), csvText(d.VehicleID), tstr(d.StartedAt), tstr(d.EndedAt), fnum(d.StartSoCPct), fnum(d.EndSoCPct),
			fnum(d.StartOdometerMi), fnum(d.EndOdometerMi), fnum(d.DistanceMi), fnum(d.EnergyUsedKWh),
			fnum(d.MaxSpeedMph), fnum(d.AvgSpeedMph), fnum(d.StartLat), fnum(d.StartLon), fnum(d.EndLat), fnum(d.EndLon),
			csvText(d.Source), csvText(d.RoutePolyline),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func writeTripsCSV(w io.Writer, e exportData) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(exportTripHeader); err != nil {
		return err
	}
	for _, t := range e.Trips {
		mix := make([]string, 0, len(t.CostSourceMix))
		for k, v := range t.CostSourceMix {
			mix = append(mix, k+"="+fnum(v))
		}
		sort.Strings(mix)
		row := []string{
			csvText(t.TripID), csvText(t.VehicleID), tstr(t.StartedAt), tstr(t.EndedAt), fnum(t.Miles), strconv.Itoa(t.FastSessions), fptr(t.MaxKMFromHome),
			fnum(t.KWhUsed), fnum(t.KWhAdded), fnum(t.Cost), fptr(t.EstimatedShare), strconv.Itoa(t.UnpricedSessions), fptr(t.EfficiencyMiKWh),
			csvText(strings.Join(t.DriveIDs, ";")), csvText(strings.Join(t.ChargeIDs, ";")), csvText(strings.Join(mix, ";")),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

const exportReadme = `Rivolt data export

charges.csv  every charging session, with the stored cost and cost_source,
             plus the ledger's effective_* columns (what the Money tab uses)
             and receipt_* (what Rivian billed, kept next to our estimate)
rivian_receipts.csv  Rivian charging receipts we stored: amount, currency,
             energy, window, vendor, city, charger name (not for home chargers)
drives.csv   every drive, including the encoded route polyline
trips.csv    auto-detected multi-day trips (ledger), with cost and mix
settings.json    your preferences (credentials are never included)
vehicles.json    per-vehicle profile (pack capacity, tires, ...)
export.json      metadata: version, time, row counts

Times are UTC (RFC 3339). Empty cells mean "not recorded". Raw telemetry
samples are not part of this export; Settings > Data > Backup has them.
`

func (e exportData) meta() map[string]any {
	return map[string]any{
		"version": e.Version, "created_at": e.CreatedAt.UTC().Format(time.RFC3339),
		"tz": e.TZ, "currency": e.Currency,
		"counts": map[string]int{"charges": len(e.Charges), "drives": len(e.Drives), "trips": len(e.Trips)},
	}
}

// writeExportZip streams the zip: each CSV is written row by row
// straight into the archive.
func writeExportZip(w io.Writer, e exportData) error {
	zw := zip.NewWriter(w)
	add := func(name string, fn func(io.Writer) error) error {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: e.CreatedAt}
		fw, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		return fn(fw)
	}
	jsonFile := func(v any) func(io.Writer) error {
		return func(w io.Writer) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		}
	}
	steps := []struct {
		name string
		fn   func(io.Writer) error
	}{
		{"README.txt", func(w io.Writer) error { _, err := io.WriteString(w, exportReadme); return err }},
		{"charges.csv", func(w io.Writer) error { return writeChargesCSV(w, e) }},
		{"drives.csv", func(w io.Writer) error { return writeDrivesCSV(w, e) }},
		{"trips.csv", func(w io.Writer) error { return writeTripsCSV(w, e) }},
		{"rivian_receipts.csv", func(w io.Writer) error { return writeReceiptsCSV(w, e) }},
		{"settings.json", jsonFile(e.Settings)},
		{"vehicles.json", jsonFile(e.Profiles)},
		{"export.json", jsonFile(e.meta())},
	}
	for _, s := range steps {
		if err := add(s.name, s.fn); err != nil {
			return err
		}
	}
	return zw.Close()
}

// writeExportJSON writes the single-document variant.
func writeExportJSON(w io.Writer, e exportData) error {
	type chargeOut struct {
		charges.Charge
		Effective ledger.Classification `json:"effective"`
	}
	cs := make([]chargeOut, len(e.Charges))
	for i, c := range e.Charges {
		cs[i].Charge = c
		if i < len(e.Classes) {
			cs[i].Effective = e.Classes[i]
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"export":           e.meta(),
		"charges":          cs,
		"drives":           e.Drives,
		"trips":            e.Trips,
		"rivian_receipts":  e.Receipts,
		"settings":         e.Settings,
		"vehicle_profiles": e.Profiles,
	})
}

// loadExport gathers one user's export (errors before any byte is
// written, so the response can still be a clean 500).
func loadExport(ctx context.Context, d Deps, uid uuid.UUID, now time.Time) (exportData, error) {
	e := exportData{Version: d.Version, CreatedAt: now, TZ: "UTC"}
	cs := d.Charges.For(uid)
	ds := d.Drives.For(uid)
	if cs == nil || ds == nil {
		return e, fmt.Errorf("stores not initialized")
	}
	var err error
	if e.Charges, err = cs.ListAll(ctx); err != nil {
		return e, fmt.Errorf("list charges: %w", err)
	}
	if e.Drives, err = ds.ListAll(ctx); err != nil {
		return e, fmt.Errorf("list drives: %w", err)
	}
	e.Settings = map[string]string{}
	var cfg ledger.Config
	if d.Settings != nil {
		if ss := d.Settings.For(uid); ss != nil {
			if m, serr := ss.GetAll(ctx); serr == nil {
				e.Settings = exportSettings(m)
			}
			if c, cerr := ledgerConfig(ctx, ss); cerr == nil {
				cfg = c
			}
		}
	}
	e.Currency = cfg.Currency
	lc := make([]ledger.Charge, len(e.Charges))
	for i, c := range e.Charges {
		lc[i] = ledger.FromCharge(c)
	}
	e.Classes = ledger.ClassifyAll(lc, cfg)

	e.Profiles = []exportProfile{}
	e.Receipts = []receipts.Receipt{}
	if d.DB != nil {
		if rs, rerr := receipts.NewStore(d.DB).ListAll(ctx, uid); rerr == nil && rs != nil {
			e.Receipts = rs
		}
		if vehs, verr := db.ListUserVehicles(ctx, d.DB, uid); verr == nil {
			for _, v := range vehs {
				p, perr := db.GetVehicleProfile(ctx, d.DB, uid, v.ID)
				if perr != nil {
					continue
				}
				e.Profiles = append(e.Profiles, exportProfile{RivianVehicleID: v.RivianVehicleID, DisplayName: v.DisplayName, Profile: p})
			}
		}
		// Trips: the ledger's own detection over all history.
		lch, lds, lerr := ledger.Load(ctx, d.DB, uid, "", time.Unix(0, 0).UTC(), now.Add(24*time.Hour))
		if lerr == nil {
			tr := ledger.BuildTrips(ledger.Input{Charges: lch, Drives: lds, Config: cfg, Now: now},
				time.Unix(0, 0).UTC(), now.Add(24*time.Hour), time.UTC)
			e.Trips = tr.Trips
		}
	}
	if e.Trips == nil {
		e.Trips = []ledger.Trip{}
	}
	return e, nil
}

// handleExport serves GET /api/export[?format=zip|json].
func handleExport(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		// "View as user" must not become a one-click download of the
		// user's full location history.
		if auth.IsImpersonating(r.Context()) {
			http.Error(w, "export is not available while viewing as another user", http.StatusForbidden)
			return
		}
		format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
		if format == "" {
			format = "zip"
		}
		if format != "zip" && format != "json" {
			http.Error(w, "format must be zip or json", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		e, err := loadExport(r.Context(), d, uid, now)
		if err != nil {
			http.Error(w, "export: "+err.Error(), http.StatusInternalServerError)
			return
		}
		stamp := now.Format("20060102-150405")
		w.Header().Set("Cache-Control", "no-store")
		if format == "json" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", `attachment; filename="rivolt-export-`+stamp+`.json"`)
			if err := writeExportJSON(w, e); err != nil {
				panic(http.ErrAbortHandler)
			}
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="rivolt-export-`+stamp+`.zip"`)
		if err := writeExportZip(w, e); err != nil {
			// Headers are gone; abort so the client sees a truncated
			// download as a failure, not a short valid file.
			panic(http.ErrAbortHandler)
		}
	}
}
