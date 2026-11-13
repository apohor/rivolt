package db

import "testing"

func TestAppliesRunnerLimits(t *testing.T) {
	for id, want := range map[string]bool{
		"0007_vehicle_state_backfill.sql": false, "0119_x.sql": false, "0120_migrations_checksum.sql": true, "0121_y": true,
		"9104_defaults": true, "junk": false, "": false,
	} {
		if got := appliesRunnerLimits(id); got != want {
			t.Errorf("%q: %v, want %v", id, got, want)
		}
	}
}
