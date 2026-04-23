package electrafi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
)

// Synthetic 2-row CSV: one driving row (shift_state=D) and one
// charging row, both tied to their own session numbers. Exercises the
// "happy path" through ImportReader without reaching for a real export.
const tinyCSV = `Date,battery_level,battery_range,odometer,latitude,longitude,speed,shift_state,charging_state,charger_power,charge_rate,charge_energy_added,charge_miles_added_rated,charge_limit_soc,driveNumber,chargeNumber
2026-03-01 10:00:00,80,240,1000,37.7749,-122.4194,35,D,Disconnected,,,,,,42,
2026-03-01 12:00:00,82,246,1010,37.7749,-122.4194,0,P,Charging,11,22,1.5,4,90,,7
`

func openStores(t *testing.T) (*drives.Store, *charges.Store, *samples.Store) {
	t.Helper()
	dir := t.TempDir()
	ds, err := drives.OpenStore(filepath.Join(dir, "drives.db"))
	if err != nil {
		t.Fatalf("open drives: %v", err)
	}
	t.Cleanup(func() { ds.Close() })
	cs, err := charges.OpenStore(filepath.Join(dir, "charges.db"))
	if err != nil {
		t.Fatalf("open charges: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	ss, err := samples.OpenStore(filepath.Join(dir, "samples.db"))
	if err != nil {
		t.Fatalf("open samples: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	return ds, cs, ss
}

func TestImportReader(t *testing.T) {
	ds, cs, ss := openStores(t)
	imp := &Importer{Drives: ds, Charges: cs, Samples: ss}

	res, err := imp.ImportReader(context.Background(), "tiny.csv", strings.NewReader(tinyCSV))
	if err != nil {
		t.Fatalf("ImportReader: %v", err)
	}
	if res.File != "tiny.csv" {
		t.Errorf("File = %q, want %q", res.File, "tiny.csv")
	}
	if res.Rows != 2 {
		t.Errorf("Rows = %d, want 2", res.Rows)
	}
	if res.Samples != 2 {
		t.Errorf("Samples = %d, want 2", res.Samples)
	}
	if res.Drives != 1 {
		t.Errorf("Drives = %d, want 1 (driveNumber=42)", res.Drives)
	}
	if res.Charges != 1 {
		t.Errorf("Charges = %d, want 1 (chargeNumber=7)", res.Charges)
	}
}

// ImportReader synthesises a vehicle ID from the provided name when
// VehicleID is blank. Passing the same name twice must produce the same
// ID so re-imports upsert cleanly.
func TestImportReaderDerivesStableVehicleID(t *testing.T) {
	ds1, cs1, ss1 := openStores(t)
	imp1 := &Importer{Drives: ds1, Charges: cs1, Samples: ss1}
	if _, err := imp1.ImportReader(context.Background(), "same.csv", strings.NewReader(tinyCSV)); err != nil {
		t.Fatalf("first import: %v", err)
	}
	got1, err := ds1.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("list drives 1: %v", err)
	}
	if len(got1) != 1 {
		t.Fatalf("len(drives)=%d, want 1", len(got1))
	}

	ds2, cs2, ss2 := openStores(t)
	imp2 := &Importer{Drives: ds2, Charges: cs2, Samples: ss2}
	if _, err := imp2.ImportReader(context.Background(), "same.csv", strings.NewReader(tinyCSV)); err != nil {
		t.Fatalf("second import: %v", err)
	}
	got2, err := ds2.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("list drives 2: %v", err)
	}
	if got1[0].VehicleID != got2[0].VehicleID {
		t.Errorf("vehicle IDs differ: %q vs %q", got1[0].VehicleID, got2[0].VehicleID)
	}
}

func TestImportReaderRejectsMissingColumns(t *testing.T) {
	ds, cs, ss := openStores(t)
	imp := &Importer{Drives: ds, Charges: cs, Samples: ss}
	// Missing 'odometer' — should fail the required-column check.
	const bad = "Date,battery_level\n2026-03-01 10:00:00,80\n"
	_, err := imp.ImportReader(context.Background(), "bad.csv", strings.NewReader(bad))
	if err == nil {
		t.Fatal("expected error for missing required columns, got nil")
	}
}
