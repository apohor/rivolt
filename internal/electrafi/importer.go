// Package electrafi imports TeslaFi / ElectraFi polling-dump CSV files
// into Rivolt's drives and charges stores.
//
// ElectraFi exports one row per poll (~60s cadence) with 144 columns of
// TeslaFi-schema snapshot data. Rivian-origin rows leave many Tesla-
// specific columns empty; the importer uses only the subset reliably
// populated by the Rivian integration:
//
//	Date, battery_level, battery_range, odometer, latitude, longitude,
//	speed, shift_state, charging_state, charger_power, charge_rate,
//	charge_energy_added, charge_miles_added_rated, charge_limit_soc,
//	driveNumber, chargeNumber
//
// Drives and charges are derived by grouping rows by driveNumber and
// chargeNumber. Row-level noise (empty cells, header) is tolerated.
package electrafi

import (
	"context"
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
)

// sampleBatchSize is the number of raw samples we buffer before writing
// to SQLite. Single large transactions are dramatically faster than
// per-row inserts for 20k-row CSVs.
const sampleBatchSize = 2000

// Result summarizes what an import produced.
type Result struct {
	File        string
	Rows        int
	Samples     int
	Drives      int
	Charges     int
	SkippedRows int
}

// Importer runs against the three stores. Samples is optional — when
// nil, only derived sessions are written.
type Importer struct {
	Drives  *drives.Store
	Charges *charges.Store
	Samples *samples.Store
	// VehicleID is the stable identifier assigned to every imported
	// session. The CSVs omit VIN so we synthesize one from the file
	// content if the operator didn't pass --vehicle-id.
	VehicleID string
	// PackKWh is the usable battery capacity, used to estimate
	// charger_power and charge_energy_added when those columns are
	// empty (ElectraFi stopped reporting them for some Rivian sessions
	// starting ~late-March 2026). Zero means "use DefaultPackKWh".
	PackKWh float64
	// Location is the timezone the CSV timestamps were recorded in.
	// ElectraFi/TeslaFi exports are local-without-zone; parsing them
	// as UTC (the pre-v0.4.2 behavior) shifts every timestamp by the
	// user's offset. Nil means UTC for backwards compatibility.
	Location *time.Location
	// OnProgress, when non-nil, is called periodically during a
	// single file import with a phase label and a row count so an
	// HTTP handler can emit heartbeats to the client. The callback
	// must not block (it's called from the row loop); the API
	// handler uses it to flush NDJSON lines.
	OnProgress func(phase string, rows int)
}

// DefaultPackKWh is the usable capacity we assume when the operator
// didn't pass a value. Matches the Rivian R1T/R1S Gen-2 Large pack
// (~131 kWh usable); adjust via Importer.PackKWh for Standard / Max.
const DefaultPackKWh = 131.0

func (i *Importer) pack() float64 {
	if i.PackKWh > 0 {
		return i.PackKWh
	}
	return DefaultPackKWh
}

func (i *Importer) loc() *time.Location {
	if i.Location != nil {
		return i.Location
	}
	return time.UTC
}

// Import reads path and upserts the derived drives & charges. Safe to
// run multiple times over the same file — session IDs are deterministic.
func (i *Importer) Import(ctx context.Context, path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	res, err := i.ImportReader(ctx, path, f)
	res.File = path
	return res, err
}

// ImportReader is the same as Import but reads from an io.Reader rather
// than a file path. Used by the HTTP upload endpoint to stream an
// uploaded CSV without staging it to disk. `name` is used only as the
// seed for the synthetic vehicle ID and for error messages; the caller
// can pass the original upload filename or any stable label.
func (i *Importer) ImportReader(ctx context.Context, name string, src io.Reader) (Result, error) {
	r := csv.NewReader(src)
	r.FieldsPerRecord = -1 // tolerate trailing-empty-column variance
	header, err := r.Read()
	if err != nil {
		return Result{}, fmt.Errorf("read header: %w", err)
	}
	idx := indexHeaders(header)
	required := []string{"Date", "battery_level", "odometer", "latitude", "longitude",
		"speed", "shift_state", "charging_state", "driveNumber", "chargeNumber"}
	for _, k := range required {
		if _, ok := idx[k]; !ok {
			return Result{}, fmt.Errorf("missing required column %q", k)
		}
	}

	vehicleID := i.VehicleID
	if vehicleID == "" {
		vehicleID = deriveVehicleID(name)
	}

	var (
		rows         int
		skipped      int
		sampleCount  int
		sampleBuf    []samples.Sample
		driveGroups  = map[string][]snapshot{}
		chargeGroups = map[string][]snapshot{}
	)
	flushSamples := func() error {
		if i.Samples == nil || len(sampleBuf) == 0 {
			sampleBuf = sampleBuf[:0]
			return nil
		}
		if err := i.Samples.InsertBatch(ctx, sampleBuf); err != nil {
			return err
		}
		sampleCount += len(sampleBuf)
		sampleBuf = sampleBuf[:0]
		return nil
	}

	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Data corruption on a single row shouldn't kill the whole
			// import — count and move on.
			skipped++
			continue
		}
		rows++
		if i.OnProgress != nil && rows%20000 == 0 {
			i.OnProgress("rows", rows)
		}

		s, ok := parseRow(row, idx, i.loc())
		if !ok {
			skipped++
			continue
		}

		if i.Samples != nil {
			sampleBuf = append(sampleBuf, samples.Sample{
				VehicleID:       vehicleID,
				At:              s.at,
				BatteryLevelPct: s.batteryLevel,
				RangeMi:         s.batteryRangeMi,
				OdometerMi:      s.odometerMi,
				Lat:             s.lat,
				Lon:             s.lon,
				SpeedMph:        s.speedMph,
				ShiftState:      s.shift,
				ChargingState:   s.chargingState,
				ChargerPowerKW:  s.chargerPowerKW,
				ChargeLimitPct:  s.chargeLimitPct,
				InsideTempC:     s.insideTempC,
				OutsideTempC:    s.outsideTempC,
				DriveNumber:     s.driveNumber,
				ChargeNumber:    s.chargeNumber,
				Source:          "electrafi_import",
			})
			if len(sampleBuf) >= sampleBatchSize {
				if err := flushSamples(); err != nil {
					return Result{}, fmt.Errorf("flush samples: %w", err)
				}
			}
		}

		// A row is attributed to a drive iff shift_state is non-P and
		// driveNumber is non-zero. Likewise for charging.
		if s.driveNumber > 0 && s.shift != "P" && s.shift != "" {
			key := groupKey(vehicleID, "d", s.driveNumber)
			driveGroups[key] = append(driveGroups[key], s)
		}
		if s.chargeNumber > 0 && isChargingState(s.chargingState) {
			key := groupKey(vehicleID, "c", s.chargeNumber)
			chargeGroups[key] = append(chargeGroups[key], s)
		}
	}
	if err := flushSamples(); err != nil {
		return Result{}, fmt.Errorf("flush samples: %w", err)
	}
	if i.OnProgress != nil {
		i.OnProgress("rows", rows)
		i.OnProgress("persist_drives", len(driveGroups))
	}

	// Persist. IDs are derived from each group's earliest-snapshot
	// timestamp, NOT from ElectraFi's driveNumber/chargeNumber — those
	// counters reset per export, so two CSVs covering overlapping date
	// ranges would otherwise produce duplicate rows for the same
	// physical session. A timestamp-keyed ID makes re-imports a clean
	// upsert.
	for _, snaps := range driveGroups {
		sort.Slice(snaps, func(i, j int) bool { return snaps[i].at.Before(snaps[j].at) })
		id := stableID(vehicleID, "d", snaps[0].at)
		// Only stamp per-drive energy when the operator set an explicit
		// --pack-kwh. The DefaultPackKWh fallback is fine for charge
		// estimation (already lossy) but we don't want to bake a guess
		// into the drive row that the dashboard will then aggregate as
		// if it were real.
		d := deriveDrive(id, vehicleID, snaps, i.PackKWh)
		if err := i.Drives.Upsert(ctx, d); err != nil {
			return Result{}, fmt.Errorf("upsert drive %s: %w", id, err)
		}
	}
	if i.OnProgress != nil {
		i.OnProgress("persist_charges", len(chargeGroups))
	}
	for _, snaps := range chargeGroups {
		sort.Slice(snaps, func(i, j int) bool { return snaps[i].at.Before(snaps[j].at) })
		id := stableID(vehicleID, "c", snaps[0].at)
		c := deriveCharge(id, vehicleID, snaps, i.pack())
		if err := i.Charges.Upsert(ctx, c); err != nil {
			return Result{}, fmt.Errorf("upsert charge %s: %w", id, err)
		}
	}

	return Result{
		File:        name,
		Rows:        rows,
		Samples:     sampleCount,
		Drives:      len(driveGroups),
		Charges:     len(chargeGroups),
		SkippedRows: skipped,
	}, nil
}

// snapshot is the minimal projection of a polling row we need.
type snapshot struct {
	at               time.Time
	batteryLevel     float64
	batteryRangeMi   float64
	odometerMi       float64
	lat, lon         float64
	speedMph         float64
	shift            string
	chargingState    string
	chargerPowerKW   float64
	chargeRateMiH    float64
	chargeEnergyKWh  float64
	chargeMilesAdded float64
	chargeLimitPct   float64
	insideTempC      float64
	outsideTempC     float64
	driveNumber      int64
	chargeNumber     int64
}

func parseRow(row []string, idx map[string]int, loc *time.Location) (snapshot, bool) {
	get := func(k string) string {
		i, ok := idx[k]
		if !ok || i >= len(row) {
			return ""
		}
		return row[i]
	}
	at, err := parseElectrafiTime(get("Date"), loc)
	if err != nil {
		return snapshot{}, false
	}
	s := snapshot{
		at:               at,
		batteryLevel:     atof(get("battery_level")),
		batteryRangeMi:   atof(get("battery_range")),
		odometerMi:       atof(get("odometer")),
		lat:              atof(get("latitude")),
		lon:              atof(get("longitude")),
		speedMph:         atof(get("speed")),
		shift:            get("shift_state"),
		chargingState:    get("charging_state"),
		chargerPowerKW:   atof(get("charger_power")),
		chargeRateMiH:    atof(get("charge_rate")),
		chargeEnergyKWh:  atof(get("charge_energy_added")),
		chargeMilesAdded: atof(get("charge_miles_added_rated")),
		chargeLimitPct:   atof(get("charge_limit_soc")),
		insideTempC:      atof(get("inside_temp")),
		outsideTempC:     atof(get("outside_temp")),
		driveNumber:      atoi(get("driveNumber")),
		chargeNumber:     atoi(get("chargeNumber")),
	}
	return s, true
}

func deriveDrive(id, vehicleID string, snaps []snapshot, packKWh float64) drives.Drive {
	if len(snaps) == 0 {
		return drives.Drive{ID: id, VehicleID: vehicleID, Source: "electrafi_import"}
	}
	first, last := snaps[0], snaps[len(snaps)-1]
	var maxSpeed, speedSum float64
	var speedN int
	for _, s := range snaps {
		if s.speedMph > maxSpeed {
			maxSpeed = s.speedMph
		}
		if s.speedMph > 0 {
			speedSum += s.speedMph
			speedN++
		}
	}
	avgSpeed := 0.0
	if speedN > 0 {
		avgSpeed = speedSum / float64(speedN)
	}
	distance := last.odometerMi - first.odometerMi
	if distance < 0 {
		distance = 0
	}
	// Pack-side energy from SoC delta × usable pack capacity. ElectraFi
	// doesn't record a drive-energy column, so this is the best proxy.
	var energy float64
	if socUsed := first.batteryLevel - last.batteryLevel; socUsed > 0 && packKWh > 0 {
		energy = socUsed / 100.0 * packKWh
	}
	return drives.Drive{
		ID:              id,
		VehicleID:       vehicleID,
		StartedAt:       first.at,
		EndedAt:         last.at,
		StartSoCPct:     first.batteryLevel,
		EndSoCPct:       last.batteryLevel,
		StartOdometerMi: first.odometerMi,
		EndOdometerMi:   last.odometerMi,
		DistanceMi:      distance,
		StartLat:        first.lat,
		StartLon:        first.lon,
		EndLat:          last.lat,
		EndLon:          last.lon,
		MaxSpeedMph:     maxSpeed,
		AvgSpeedMph:     avgSpeed,
		EnergyUsedKWh:   energy,
		Source:          "electrafi_import",
	}
}

func deriveCharge(id, vehicleID string, snaps []snapshot, packKWh float64) charges.Charge {
	if len(snaps) == 0 {
		return charges.Charge{ID: id, VehicleID: vehicleID, Source: "electrafi_import"}
	}
	first, last := snaps[0], snaps[len(snaps)-1]
	var maxPower float64
	var maxEnergy, maxMiles float64
	for _, s := range snaps {
		if s.chargerPowerKW > maxPower {
			maxPower = s.chargerPowerKW
		}
		if s.chargeEnergyKWh > maxEnergy {
			maxEnergy = s.chargeEnergyKWh
		}
		if s.chargeMilesAdded > maxMiles {
			maxMiles = s.chargeMilesAdded
		}
	}
	// Session average = energy delivered ÷ wall-clock duration. Folds
	// in ramp-up and taper, and stays consistent with EnergyAddedKWh
	// and Duration on the row. We used to compute Σ(charger_power)/N
	// over only ticks where charger_power > 0, but that excludes
	// ramp/taper and isn't physically meaningful.
	avgPower := 0.0
	if hours := last.at.Sub(first.at).Hours(); hours > 0 && maxEnergy > 0 {
		avgPower = maxEnergy / hours
	}

	// Fallback: ElectraFi occasionally stops reporting charger_power /
	// charge_energy_added (observed for every Rivian session after
	// 2026-03-24 in our sample data). When both are zero, estimate from
	// battery_level delta — it's the only signal we have left.
	//
	// We derive energy from (endSoC - startSoC)/100 * packKWh and power
	// from energy / elapsed_hours. We deliberately do NOT use the max of
	// instantaneous dSoC/dt — battery_level is quantised to ~0.01-0.1%
	// and a single tick over a short interval produces a spurious peak.
	// Sessions that hit this fallback are always AC home charging (DC
	// fast-charge sessions kept their real charger_power in the sample
	// data), so the average *is* the peak — the charger delivers a
	// steady ~7.5 kW for hours.
	if maxEnergy == 0 && maxPower == 0 && packKWh > 0 {
		dSoC := last.batteryLevel - first.batteryLevel
		if dSoC > 0 {
			maxEnergy = dSoC / 100.0 * packKWh
		}
		totalHours := last.at.Sub(first.at).Hours()
		if totalHours > 0 && maxEnergy > 0 {
			avgPower = maxEnergy / totalHours
			maxPower = avgPower
		}
	}

	// ElectraFi's charge_energy_added and charge_miles_added_rated are
	// running totals within the session, so the max across the session
	// is the delivered amount.
	return charges.Charge{
		ID:             id,
		VehicleID:      vehicleID,
		StartedAt:      first.at,
		EndedAt:        last.at,
		StartSoCPct:    first.batteryLevel,
		EndSoCPct:      last.batteryLevel,
		EnergyAddedKWh: maxEnergy,
		MilesAdded:     maxMiles,
		MaxPowerKW:     maxPower,
		AvgPowerKW:     avgPower,
		FinalState:     last.chargingState,
		Lat:            first.lat,
		Lon:            first.lon,
		Source:         "electrafi_import",
	}
}

func indexHeaders(h []string) map[string]int {
	m := make(map[string]int, len(h))
	for i, k := range h {
		m[strings.TrimSpace(k)] = i
	}
	return m
}

// parseElectrafiTime accepts the "2026-01-01 00:00:43" format used
// across the export. Timestamps are local-without-zone; the caller
// passes the location the export was recorded in (Importer.Location,
// defaulting to UTC for back-compat).
func parseElectrafiTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty time")
	}
	if loc == nil {
		loc = time.UTC
	}
	return time.ParseInLocation("2006-01-02 15:04:05", s, loc)
}

func atof(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func atoi(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func groupKey(vehicleID, kind string, n int64) string {
	return fmt.Sprintf("electrafi_%s_%s_%d", vehicleID, kind, n)
}

// stableID builds a deterministic row ID from the session's start
// timestamp. Two CSVs covering overlapping date ranges will produce
// the same ID for the same physical session so the upsert collapses
// them instead of creating a duplicate. Seconds precision is enough —
// ElectraFi's poll cadence (~60 s) makes first-row collisions the
// common case even when the exports don't start on the same row.
func stableID(vehicleID, kind string, t time.Time) string {
	return fmt.Sprintf("electrafi_%s_%s_%d", vehicleID, kind, t.UTC().Unix())
}

// isChargingState returns true for any state that ElectraFi emits while
// a session is ongoing or has just ended. We include Complete so the
// final row's state is captured as the session terminator.
func isChargingState(s string) bool {
	switch s {
	case "Charging", "Complete", "charging_ready", "charging_connecting",
		"waiting_on_charger", "charging_station_err", "charging_user_stoppe":
		return true
	}
	return false
}

// deriveVehicleID synthesizes a stable ID from the file path when the
// operator didn't pass --vehicle-id. Hash first so that re-imports from
// the same file produce the same vehicle_id.
func deriveVehicleID(path string) string {
	h := sha1.Sum([]byte(path))
	return "electrafi-" + hex.EncodeToString(h[:])[:12]
}
