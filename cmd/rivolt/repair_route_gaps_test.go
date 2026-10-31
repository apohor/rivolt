package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/repair/routegaps"
)

// The safety rails are checked before anything is opened: --apply without the approved dry run's numbers, a backup or a Valhalla
// URL does nothing, and neither do bad times or a gap below the recorder's own threshold.
func TestRepairRouteGapsRefusesBeforeTouchingAnything(t *testing.T) {
	good := []string{"--apply", "--expect-count", "3", "--expect-sha", "abc", "--backup", filepath.Join(t.TempDir(), "b.csv"), "--valhalla-url", "http://valhalla.invalid"}
	without := func(flag string) []string {
		var out []string
		for i := 0; i < len(good); i++ {
			if good[i] == flag {
				if flag != "--apply" {
					i++
				}
				continue
			}
			out = append(out, good[i])
		}
		return out
	}
	for name, args := range map[string][]string{
		"no expect-count": without("--expect-count"),
		"no expect-sha":   without("--expect-sha"),
		"no backup":       without("--backup"),
		"no valhalla":     without("--valhalla-url"),
		"bad since":       {"--since", "yesterday"},
		"bad until":       {"--until", "2026-13-40"},
		"tiny gap":        {"--min-gap-m", "50"},
		"public valhalla": withValhalla(good, "https://valhalla1.openstreetmap.de"),
		"public ip":       withValhalla(good, "http://8.8.8.8:8002"),
		"unknown flag":    {"--nope"},
	} {
		t.Setenv("RIVOLT_VALHALLA_BASE_URL", "")
		if code := runRepairRouteGaps(args); code != 2 {
			t.Errorf("%s: exit %d, want 2 (refused before opening the database)", name, code)
		}
	}
}

// withValhalla is args with the --valhalla-url value replaced.
func withValhalla(args []string, url string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--valhalla-url" {
			out[i+1] = url
		}
	}
	return out
}

func TestParseRepairTime(t *testing.T) {
	def := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"":                          def,
		"2026-10-01":                time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		"2026-10-01T05:00:00-05:00": time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC),
	} {
		if got, err := parseRepairTime(in, def); err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := parseRepairTime("tomorrow", def); err == nil {
		t.Error("tomorrow parsed")
	}
}

func TestRouteGapsBackupRoundTripsAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.csv")
	c, err := newRouteGapsCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	row := routegaps.BackupRow{DriveID: uuid.New(), UserID: uuid.New(), Old: "_p~iF~ps|U_ulLnnqC,\"quoted\"", NewSHA: "ab12"}
	if err := c.write(row); err != nil {
		t.Fatal(err)
	}
	if err := c.f.Close(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v, want 0600", info.Mode().Perm())
	}
	got, err := readRouteGapsCSV(path)
	if err != nil || len(got) != 1 || got[0] != row {
		t.Fatalf("read back %+v %v, want %+v", got, err, row)
	}
	if _, err := newRouteGapsCSV(path); err == nil {
		t.Error("a second backup overwrote the first")
	}
}
