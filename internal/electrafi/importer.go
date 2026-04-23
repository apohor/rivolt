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

		s, ok := parseRow(row, idx)
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

	// Persist.
	for id, snaps := range driveGroups {
		sort.Slice(snaps, func(i, j int) bool { return snaps[i].at.Before(snaps[j].at) })
		d := deriveDrive(id, vehicleID, snaps)
		if err := i.Drives.Upsert(ctx, d); err != nil {
			return Result{}, fmt.Errorf("upsert drive %s: %w", id, err)
		}
	}
	for id, snaps := range chargeGroups {
		sort.Slice(snaps, func(i, j int) bool { return snaps[i].at.Before(snaps[j].at) })
		c := deriveCharge(id, vehicleID, snaps)
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

func parseRow(row []string, idx map[string]int) (snapshot, bool) {
	get := func(k string) string {
		i, ok := idx[k]
		if !ok || i >= len(row) {
			return ""
		}
		return row[i]
	}
	at, err := parseElectrafiTime(get("Date"))
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

func deriveDrive(id, vehicleID string, snaps []snapshot) drives.Drive {
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
		Source:          "electrafi_import",
	}
}

func deriveCharge(id, vehicleID string, snaps []snapshot) charges.Charge {
	if len(snaps) == 0 {
		return charges.Charge{ID: id, VehicleID: vehicleID, Source: "electrafi_import"}
	}
	first, last := snaps[0], snaps[len(snaps)-1]
	var maxPower, powerSum float64
	var powerN int
	var maxEnergy, maxMiles float64
	for _, s := range snaps {
		if s.chargerPowerKW > maxPower {
			maxPower = s.chargerPowerKW
		}
		if s.chargerPowerKW > 0 {
			powerSum += s.chargerPowerKW
			powerN++
		}
		if s.chargeEnergyKWh > maxEnergy {
			maxEnergy = s.chargeEnergyKWh
		}
		if s.chargeMilesAdded > maxMiles {
			maxMiles = s.chargeMilesAdded
		}
	}
	avgPower := 0.0
	if powerN > 0 {
		avgPower = powerSum / float64(powerN)
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
// across the export. Timestamps are local-without-zone; we treat them
// as UTC since the export is timezone-opaque.
func parseElectrafiTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty time")
	}
	return time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
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
