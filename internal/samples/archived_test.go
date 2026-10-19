package samples

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/archive"
)

// An archived row maps to the Sample EachAll would give for the same database
// row: NULL numbers and text become 0 and "", the nullable readings nil.
func TestSampleFromArchive(t *testing.T) {
	header := []string{"vehicle_id", "at", "user_id", "battery_level_pct", "range_mi", "odometer_mi", "lat", "lon",
		"speed_mph", "shift_state", "charging_state", "charger_power_kw", "charge_limit_pct", "inside_temp_c",
		"outside_temp_c", "drive_number", "charge_number", "source", "raw", "location_fix_at", "altitude_m",
		"drive_mode", "tire_pressure_min_bar", "pack_temp_avg_c", "pack_temp_max_c", "pack_temp_min_c", "power_state", "charger_status"}
	rec := []string{"6f1c2a52-0000-4000-8000-000000000001", "2026-05-02T10:00:00.250000Z", "u", "81.5", "", "12345.6", "30.1", "-97.7",
		"", "D", "", "", "80", "", "21.5", "7", "", "rivian", "", "2026-05-02T09:59:59.000000Z", "",
		"everyday", "2.4", "", "", "", "ready", ""}
	v, err := sampleFromArchive(archive.NewRow(header, rec), "RV-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 5, 2, 10, 0, 0, 250000000, time.UTC)
	if v.VehicleID != "RV-1" || !v.At.Equal(at) || v.BatteryLevelPct != 81.5 || v.RangeMi != 0 || v.OdometerMi != 12345.6 ||
		v.Lat != 30.1 || v.Lon != -97.7 || v.SpeedMph != 0 || v.ShiftState != "D" || v.ChargingState != "" ||
		v.ChargeLimitPct != 80 || v.InsideTempC != 0 || v.OutsideTempC != 21.5 || v.DriveNumber != 7 || v.ChargeNumber != 0 ||
		v.Source != "rivian" || v.DriveMode != "everyday" {
		t.Errorf("sample %+v", v)
	}
	if v.LocationFixAt == nil || !v.LocationFixAt.Equal(time.Date(2026, 5, 2, 9, 59, 59, 0, time.UTC)) {
		t.Errorf("location_fix_at %v", v.LocationFixAt)
	}
	if v.AltitudeM != nil || v.PackTempAvgC != nil || v.TirePressureMinBar == nil || *v.TirePressureMinBar != 2.4 {
		t.Errorf("nullable readings: alt %v pack %v tire %v", v.AltitudeM, v.PackTempAvgC, v.TirePressureMinBar)
	}
	// EachAll doesn't carry power_state/charger_status; neither does this.
	if v.PowerState != "" || v.ChargerStatus != "" {
		t.Errorf("power_state %q charger_status %q", v.PowerState, v.ChargerStatus)
	}
	rec[3] = "not-a-number"
	if _, err := sampleFromArchive(archive.NewRow(header, rec), "RV-1"); err == nil {
		t.Error("a bad number was accepted")
	}
}
