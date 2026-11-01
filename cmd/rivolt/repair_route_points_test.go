package main

import (
	"path/filepath"
	"testing"
)

// The safety rails are checked before anything is opened: --apply without the approved dry run's numbers or a backup does nothing, and neither
// do bad times or an unknown flag.
func TestRepairRoutePointsRefusesBeforeTouchingAnything(t *testing.T) {
	backup := filepath.Join(t.TempDir(), "b.csv")
	for name, args := range map[string][]string{
		"no expect-count": {"--apply", "--expect-sha", "abc", "--backup", backup},
		"no expect-sha":   {"--apply", "--expect-count", "3", "--backup", backup},
		"no backup":       {"--apply", "--expect-count", "3", "--expect-sha", "abc"},
		"bare apply":      {"--apply"},
		"bad since":       {"--since", "yesterday"},
		"bad until":       {"--until", "2026-13-40"},
		"unknown flag":    {"--nope"},
	} {
		if code := runRepairRoutePoints(args); code != 2 {
			t.Errorf("%s: exit %d, want 2 (refused before opening the database)", name, code)
		}
	}
}
