package plugreminder

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/engage"
)

const homeLat, homeLon = 34.1613, -118.1676

type fakeSource struct {
	cands     []Candidate
	home      bool
	snaps     []Snap
	plugged   bool
	homeCalls int
}

func (f *fakeSource) Candidates(context.Context) ([]Candidate, error) { return f.cands, nil }
func (f *fakeSource) Home(context.Context, uuid.UUID) (float64, float64, bool, error) {
	f.homeCalls++
	return homeLat, homeLon, f.home, nil
}
func (f *fakeSource) Latest(context.Context, uuid.UUID, time.Time) ([]Snap, error) {
	return f.snaps, nil
}
func (f *fakeSource) RecentlyPlugged(context.Context, uuid.UUID, float64, float64, time.Time) (bool, error) {
	return f.plugged, nil
}

// engageStore is the claim table, in memory.
type engageStore struct {
	mu     sync.Mutex
	tz     string
	stored bool
	rows   map[string]string
}

func (e *engageStore) Timezone(context.Context, uuid.UUID) (string, bool, error) {
	return e.tz, e.stored, nil
}
func (e *engageStore) BudgetUsed(context.Context, uuid.UUID, time.Time) (int, error) { return 99, nil }
func (e *engageStore) Claim(_ context.Context, m engage.Msg, _, _ time.Time, _ int) (bool, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	k := m.Kind + "|" + m.Key
	if _, ok := e.rows[k]; ok {
		return false, false, nil
	}
	e.rows[k] = "sending"
	return true, false, nil
}
func (e *engageStore) Mark(_ context.Context, _ uuid.UUID, kind, key, status string, _ time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rows[kind+"|"+key] = status
	return nil
}
func (e *engageStore) RecordSkip(context.Context, uuid.UUID, string, string, engage.Channel, string, time.Time) (bool, error) {
	return true, nil
}
func (e *engageStore) StuckSending(context.Context, time.Duration, time.Time) (int, error) {
	return 0, nil
}
func (e *engageStore) Purge(context.Context, time.Time) (int64, error) { return 0, nil }

type env struct {
	src       *fakeSource
	es        *engageStore
	job       *Job
	delivered []int
	on        bool
	now       time.Time
}

// 2026-10-21 03:00 UTC is 20:00 on the 20th in Los Angeles (PDT).
func newEnv() *env {
	e := &env{on: true, now: time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)}
	uid := uuid.New()
	e.src = &fakeSource{home: true, cands: []Candidate{{User: uid, Timezone: "America/Los_Angeles", ThresholdPct: 40, RemindAtMin: 20 * 60}}}
	e.src.snaps = []Snap{{Vehicle: "v", At: e.now.Add(-2 * time.Minute), SoCPct: 31.6, Lat: homeLat, Lon: homeLon, Gear: "P"}}
	e.es = &engageStore{tz: "America/Los_Angeles", stored: true, rows: map[string]string{}}
	e.job = &Job{
		Source: e.src, Engage: &engage.Engage{Store: e.es, Now: func() time.Time { return e.now }},
		Now: func() time.Time { return e.now },
		On:  func(context.Context, uuid.UUID) (bool, error) { return e.on, nil },
		Deliver: func(_ context.Context, _ uuid.UUID, soc, th int) error {
			e.delivered = append(e.delivered, soc, th)
			return nil
		},
	}
	return e
}

func TestReminderFiresOnceADay(t *testing.T) {
	e := newEnv()
	st, err := e.job.Sweep(context.Background())
	if err != nil || st.Sent != 1 || len(e.delivered) != 2 || e.delivered[0] != 32 || e.delivered[1] != 40 {
		t.Fatalf("stats %+v err %v delivered %v", st, err, e.delivered)
	}
	if _, ok := e.es.rows["plug_in_reminder|2026-10-20"]; !ok {
		t.Errorf("claim rows %v, want the local date 2026-10-20", e.es.rows)
	}
	// The next sweeps, 10 and 20 minutes later, do not send again.
	for i := 0; i < 2; i++ {
		e.now = e.now.Add(10 * time.Minute)
		e.src.snaps[0].At = e.now.Add(-time.Minute)
		if st, _ := e.job.Sweep(context.Background()); st.Sent != 0 || len(e.delivered) != 2 {
			t.Errorf("sweep %d sent again: %+v", i, st)
		}
	}
	// Tomorrow it can.
	e.now = e.now.Add(24 * time.Hour)
	e.src.snaps[0].At = e.now.Add(-time.Minute)
	if st, _ := e.job.Sweep(context.Background()); st.Sent != 1 {
		t.Errorf("next day: %+v", st)
	}
}

func TestReminderRules(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*env)
		want   int
	}{
		"baseline":                  {func(*env) {}, 1},
		"before the remind time":    {func(e *env) { e.now = e.now.Add(-30 * time.Minute) }, 0},
		"at 22:00 local":            {func(e *env) { e.now = e.now.Add(2 * time.Hour) }, 0},
		"21:50 local":               {func(e *env) { e.now = e.now.Add(110 * time.Minute); e.src.snaps[0].At = e.now.Add(-time.Minute) }, 1},
		"no home place":             {func(e *env) { e.src.home = false }, 0},
		"state is 20 minutes old":   {func(e *env) { e.src.snaps[0].At = e.now.Add(-20 * time.Minute) }, 0},
		"driving":                   {func(e *env) { e.src.snaps[0].Gear = "D"; e.src.snaps[0].SpeedMph = 30 }, 0},
		"gear blank and standing":   {func(e *env) { e.src.snaps[0].Gear = "" }, 1},
		"away from home":            {func(e *env) { e.src.snaps[0].Lat += 0.01 }, 0},
		"within 150 m":              {func(e *env) { e.src.snaps[0].Lat += 0.001 }, 1},
		"plugged in":                {func(e *env) { e.src.snaps[0].Plugged = true }, 0},
		"at the threshold":          {func(e *env) { e.src.snaps[0].SoCPct = 40 }, 0},
		"above the threshold":       {func(e *env) { e.src.snaps[0].SoCPct = 80 }, 0},
		"plugged in within 2 hours": {func(e *env) { e.src.plugged = true }, 0},
		"switch turned off":         {func(e *env) { e.on = false }, 0},
		"no zone stored":            {func(e *env) { e.es.stored = false }, 0},
		"a second truck, still low": {func(e *env) {
			e.src.snaps = append(e.src.snaps, Snap{Vehicle: "w", At: e.now, SoCPct: 12, Lat: homeLat, Lon: homeLon, Gear: "P"})
		}, 1},
		"one truck home and plugged": {func(e *env) {
			e.src.snaps = []Snap{{Vehicle: "v", At: e.now, SoCPct: 20, Lat: homeLat, Lon: homeLon, Gear: "P", Plugged: true}}
		}, 0},
	} {
		e := newEnv()
		tc.mutate(e)
		st, err := e.job.Sweep(context.Background())
		if err != nil || st.Sent != tc.want {
			t.Errorf("%s: sent %d (%v), want %d", name, st.Sent, err, tc.want)
		}
	}
	// The lowest charge among the trucks is the one reported.
	e := newEnv()
	e.src.snaps = append(e.src.snaps, Snap{Vehicle: "w", At: e.now, SoCPct: 12, Lat: homeLat, Lon: homeLon, Gear: "P"})
	e.job.Sweep(context.Background())
	if len(e.delivered) != 2 || e.delivered[0] != 12 {
		t.Errorf("delivered %v, want 12%%", e.delivered)
	}
}

func TestOutsideTheWindowReadsNothing(t *testing.T) {
	e := newEnv()
	e.now = e.now.Add(-6 * time.Hour) // 14:00 local
	e.job.Sweep(context.Background())
	if e.src.homeCalls != 0 {
		t.Error("an account outside its window was read")
	}
}

func TestDeliveryFailureIsRecordedNotRetried(t *testing.T) {
	e := newEnv()
	e.job.Deliver = func(context.Context, uuid.UUID, int, int) error { return errors.New("push down") }
	if st, _ := e.job.Sweep(context.Background()); st.Sent != 0 || e.es.rows["plug_in_reminder|2026-10-20"] != "failed" {
		t.Fatalf("rows %v stats %+v", e.es.rows, st)
	}
	e.job.Deliver = func(context.Context, uuid.UUID, int, int) error { t.Error("retried"); return nil }
	e.now = e.now.Add(10 * time.Minute)
	e.src.snaps[0].At = e.now
	e.job.Sweep(context.Background())
}
