package samples

import (
	"errors"
	"testing"
	"time"
)

var winNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func at(t time.Time) []Sample { return []Sample{{VehicleID: "v", At: t}} }

func TestCheckBatchWindow(t *testing.T) {
	for name, tc := range map[string]struct {
		t  time.Time
		ok bool
	}{
		"now":                 {winNow, true},
		"the first day":       {WindowStart, true},
		"a minute before":     {WindowStart.Add(-time.Minute), false},
		"year 1":              {time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), false},
		"the zero time":       {time.Time{}, false},
		"23 hours ahead":      {winNow.Add(23 * time.Hour), true},
		"25 hours ahead":      {winNow.Add(25 * time.Hour), false},
		"year 9999":           {time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), false},
		"year 2999 in a zone": {time.Date(2999, 6, 1, 0, 0, 0, 0, time.FixedZone("x", 5*3600)), false},
	} {
		_, err := checkBatch(at(tc.t), winNow)
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, ErrSampleOutOfWindow)) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// One bad sample among good ones refuses the batch, and nothing is partitioned.
	batch := []Sample{{At: winNow}, {At: winNow.Add(-time.Hour)}, {At: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if m, err := checkBatch(batch, winNow); err == nil || m != nil {
		t.Errorf("mixed batch: %v %v", m, err)
	}
	if m, err := checkBatch(batch[:2], winNow); err != nil || len(m) != 1 {
		t.Errorf("good batch: %v %v", m, err)
	}
}

func TestCheckBatchCapsTheMonths(t *testing.T) {
	var b []Sample
	for i := 0; i < MaxBatchMonths; i++ {
		b = append(b, Sample{At: WindowStart.AddDate(0, i, 0)})
	}
	if m, err := checkBatch(b, winNow.AddDate(20, 0, 0)); err != nil || len(m) != MaxBatchMonths {
		t.Fatalf("%d months: %d %v", MaxBatchMonths, len(m), err)
	}
	b = append(b, Sample{At: WindowStart.AddDate(0, MaxBatchMonths, 0)})
	if _, err := checkBatch(b, winNow.AddDate(20, 0, 0)); !errors.Is(err, ErrTooManyMonths) {
		t.Errorf("%d months: %v, want ErrTooManyMonths", MaxBatchMonths+1, err)
	}
}
