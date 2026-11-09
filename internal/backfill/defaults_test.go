package backfill

import (
	"testing"
	"time"
)

// DA's final pace: 1000 rows a batch, one batch per 10 seconds (about 3 hours per million rows).
func TestDefaultPace(t *testing.T) {
	if DefaultBatch != 1000 || DefaultPause != 10*time.Second {
		t.Errorf("defaults %d rows / %v, want 1000 rows / 10 s", DefaultBatch, DefaultPause)
	}
	hours := float64(1_000_000/DefaultBatch) * DefaultPause.Hours()
	if hours < 2.5 || hours > 3.5 {
		t.Errorf("a million rows would take %.1f h at the default pace, want about 3", hours)
	}
}
