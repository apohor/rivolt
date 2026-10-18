package engage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type row struct {
	status   string
	budgeted bool
	at       time.Time
}

type fakeStore struct {
	mu      sync.Mutex
	tz      string
	stored  bool
	rows    map[[2]string]row
	failMrk bool
}

func newFake(tz string, stored bool) *fakeStore {
	return &fakeStore{tz: tz, stored: stored, rows: map[[2]string]row{}}
}

func (f *fakeStore) Timezone(context.Context, uuid.UUID) (string, bool, bool, error) {
	return f.tz, f.stored, f.tz != "", nil
}
func (f *fakeStore) used(since time.Time) int {
	n := 0
	for _, r := range f.rows {
		if r.budgeted && (r.status == "sending" || r.status == "sent") && r.at.After(since) {
			n++
		}
	}
	return n
}
func (f *fakeStore) BudgetUsed(_ context.Context, _ uuid.UUID, since time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.used(since), nil
}
func (f *fakeStore) Claim(_ context.Context, m Msg, now, since time.Time, limit int) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := [2]string{m.Kind, m.Key}
	if _, ok := f.rows[k]; ok {
		return false, false, nil
	}
	if !m.Utility && f.used(since) >= limit {
		return false, true, nil
	}
	f.rows[k] = row{"sending", !m.Utility, now}
	return true, false, nil
}
func (f *fakeStore) Mark(_ context.Context, _ uuid.UUID, kind, key, status string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMrk {
		return errors.New("db down")
	}
	k := [2]string{kind, key}
	r := f.rows[k]
	r.status = status
	f.rows[k] = r
	return nil
}
func (f *fakeStore) RecordSkip(_ context.Context, _ uuid.UUID, kind, key string, _ Channel, status string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := [2]string{kind, key}
	if _, ok := f.rows[k]; ok {
		return false, nil
	}
	f.rows[k] = row{status, false, now}
	return true, nil
}
func (f *fakeStore) StuckSending(context.Context, time.Duration, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeStore) Purge(context.Context, time.Time) (int64, error) { return 0, nil }

var on = func(context.Context) (bool, error) { return true, nil }

func at(h int) time.Time { return time.Date(2026, 10, 21, h, 0, 0, 0, time.UTC) }

func engine(f *fakeStore, now time.Time) (*Engage, *[]string) {
	var seen []string
	return &Engage{Store: f, Now: func() time.Time { return now }, Observe: func(k, o string) { seen = append(seen, k+":"+o) }}, &seen
}

func send(e *Engage, m Msg, delivered *int) (string, error) {
	return e.Send(context.Background(), m, func(context.Context) error { *delivered++; return nil })
}

func TestSendChecksInOrder(t *testing.T) {
	uid := uuid.New()
	push := func(key string) Msg {
		return Msg{User: uid, Kind: "milestone", Key: key, Channel: Push, Toggle: on}
	}
	var n int

	// Toggle off (or absent) wins over everything and leaves no row.
	f := newFake("UTC", false)
	e, _ := engine(f, at(12))
	off := push("a")
	off.Toggle = func(context.Context) (bool, error) { return false, nil }
	if out, _ := send(e, off, &n); out != SkippedOff {
		t.Errorf("off: %s", out)
	}
	none := push("a")
	none.Toggle = nil
	if out, _ := send(e, none, &n); out != SkippedOff || len(f.rows) != 0 || n != 0 {
		t.Errorf("nil toggle: %s rows %d delivered %d", out, len(f.rows), n)
	}

	// Quiet hours: pushes only 09:00-20:00 local. 21:00 UTC is 16:00 in Chicago (CDT).
	for _, tc := range []struct {
		tz   string
		hour int
		want string
	}{
		{"UTC", 8, SkippedQuiet}, {"UTC", 9, Sent}, {"UTC", 19, Sent}, {"UTC", 20, SkippedQuiet},
		{"America/Chicago", 21, Sent},        // 16:00 local
		{"America/Chicago", 3, SkippedQuiet}, // 22:00 local
	} {
		f := newFake(tc.tz, true)
		e, _ := engine(f, at(tc.hour))
		if out, err := send(e, push("k"), &n); out != tc.want || err != nil {
			t.Errorf("%s %02d:00 UTC: %s (%v), want %s", tc.tz, tc.hour, out, err, tc.want)
		}
	}
	// A quiet skip leaves no claim: the same key can go out in the window.
	f = newFake("UTC", false)
	e, _ = engine(f, at(7))
	send(e, push("w"), &n)
	e.Now = func() time.Time { return at(10) }
	if out, _ := send(e, push("w"), &n); out != Sent {
		t.Errorf("after quiet hours the same key: %s", out)
	}
	// Email has no quiet hours.
	f = newFake("UTC", false)
	e, _ = engine(f, at(3))
	em := push("m")
	em.Channel = Email
	if out, _ := send(e, em, &n); out != Sent {
		t.Errorf("email at 03:00: %s", out)
	}
}

func TestBudgetAndIdempotency(t *testing.T) {
	uid := uuid.New()
	f := newFake("UTC", true)
	e, seen := engine(f, at(12))
	var n int
	msg := func(kind, key string) Msg { return Msg{User: uid, Kind: kind, Key: key, Channel: Email, Toggle: on} }

	if out, _ := send(e, msg("recap", "w1"), &n); out != Sent {
		t.Fatalf("first: %s", out)
	}
	if out, _ := send(e, msg("recap", "w1"), &n); out != AlreadySent || n != 1 {
		t.Errorf("same key twice: %s delivered %d", out, n)
	}
	for _, k := range []string{"2", "3", "4"} {
		if out, _ := send(e, msg("milestone", k), &n); out != Sent {
			t.Fatalf("send %s: %s", k, out)
		}
	}
	if out, _ := send(e, msg("milestone", "5"), &n); out != SkippedBudget || n != 4 {
		t.Errorf("fifth in 30 days: %s delivered %d", out, n)
	}
	// A utility is outside the budget, and is not counted by it.
	util := Msg{User: uid, Kind: "plug_in_reminder", Key: "2026-10-21", Channel: Push, Utility: true, Toggle: on}
	if out, _ := send(e, util, &n); out != Sent {
		t.Errorf("utility over budget: %s", out)
	}
	// Thirty-one days later the window has rolled.
	e.Now = func() time.Time { return at(12).Add(31 * 24 * time.Hour) }
	if out, _ := send(e, msg("milestone", "6"), &n); out != Sent {
		t.Errorf("after the window: %s", out)
	}
	if len(*seen) == 0 {
		t.Error("no metric observations")
	}
}

func TestUtilityNeedsAStoredZoneAndTheDaytime(t *testing.T) {
	uid := uuid.New()
	var n int
	util := Msg{User: uid, Kind: "plug_in_reminder", Key: "d", Channel: Push, Utility: true, Toggle: on}

	// No zone stored by the browser: no guess.
	e, _ := engine(newFake("UTC", false), at(20))
	if out, _ := send(e, util, &n); out != SkippedNoTZ {
		t.Errorf("no stored zone: %s", out)
	}
	// 20:00 local goes; 22:00 and 06:00 local do not (a utility is not held to 09-20).
	for _, tc := range []struct {
		hour int
		want string
	}{{20, Sent}, {21, Sent}, {22, SkippedWindow}, {23, SkippedWindow}, {3, SkippedWindow}, {6, SkippedWindow}, {7, Sent}} {
		e, _ := engine(newFake("UTC", true), at(tc.hour))
		util.Key = "k" + string(rune('a'+tc.hour))
		if out, _ := send(e, util, &n); out != tc.want {
			t.Errorf("%02d:00 local: %s, want %s", tc.hour, out, tc.want)
		}
	}
	// An unknown zone name on a utility is not a guess either.
	e, _ = engine(newFake("Mars/Base", true), at(20))
	util.Key = "z"
	if out, _ := send(e, util, &n); out != SkippedNoTZ {
		t.Errorf("bad zone: %s", out)
	}
}

func TestFailedDeliveryIsRecordedAndNotRetried(t *testing.T) {
	uid := uuid.New()
	f := newFake("UTC", true)
	e, _ := engine(f, at(12))
	m := Msg{User: uid, Kind: "recap", Key: "w", Channel: Email, Toggle: on}
	boom := errors.New("smtp down")
	out, err := e.Send(context.Background(), m, func(context.Context) error { return boom })
	if out != Failed || !errors.Is(err, boom) || f.rows[[2]string{"recap", "w"}].status != "failed" {
		t.Fatalf("%s %v %v", out, err, f.rows)
	}
	var n int
	if out, _ := send(e, m, &n); out != AlreadySent || n != 0 {
		t.Errorf("a failed claim was retried under the same key: %s delivered %d", out, n)
	}
	// A failed row does not count toward the budget.
	if used, _ := f.BudgetUsed(context.Background(), uid, at(12).Add(-time.Hour)); used != 0 {
		t.Errorf("failed counted: %d", used)
	}
}

func TestSkipRecordsOnceAndInvalidInputIsRefused(t *testing.T) {
	uid := uuid.New()
	f := newFake("UTC", true)
	e, _ := engine(f, at(12))
	if ok, err := e.Skip(context.Background(), uid, "recap", "w", Email, StatusEmpty); !ok || err != nil {
		t.Fatalf("skip: %v %v", ok, err)
	}
	if ok, _ := e.Skip(context.Background(), uid, "recap", "w", Email, StatusEmpty); ok {
		t.Error("second skip created a row")
	}
	var n int
	if out, _ := send(e, Msg{User: uid, Kind: "recap", Key: "w", Channel: Email, Toggle: on}, &n); out != AlreadySent || n != 0 {
		t.Errorf("a skipped week was sent: %s", out)
	}
	if _, err := e.Skip(context.Background(), uid, "x", "y", Email, "sent"); err == nil {
		t.Error("Skip accepted a non-skip status")
	}
	for _, bad := range []Msg{
		{Kind: "k", Key: "k", Channel: Push, Toggle: on},
		{User: uid, Key: "k", Channel: Push, Toggle: on},
		{User: uid, Kind: "k", Channel: Push, Toggle: on},
		{User: uid, Kind: "k", Key: "k", Channel: "sms", Toggle: on},
	} {
		if _, err := send(e, bad, &n); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// A push to an account with no known zone (neither its browser's nor the
// statement email's) is refused rather than sent at UTC hours; an email has no
// clock to keep.
func TestBudgetedPushNeedsAZone(t *testing.T) {
	uid := uuid.New()
	var n int
	f := newFake("", false)
	e, _ := engine(f, at(12))
	push := Msg{User: uid, Kind: "milestone", Key: "a", Channel: Push, Toggle: on}
	if out, _ := send(e, push, &n); out != SkippedNoTZ || n != 0 || len(f.rows) != 0 {
		t.Errorf("push with no zone: %s delivered %d rows %d", out, n, len(f.rows))
	}
	em := Msg{User: uid, Kind: "recap", Key: "w", Channel: Email, Toggle: on}
	if out, _ := send(e, em, &n); out != Sent {
		t.Errorf("email with no zone: %s", out)
	}
	// A zone known only from the statement email is enough for quiet hours.
	g := newFake("America/Chicago", false)
	e2, _ := engine(g, at(20)) // 15:00 local
	if out, _ := send(e2, push, &n); out != Sent {
		t.Errorf("push with the statement zone: %s", out)
	}
}
