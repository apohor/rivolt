package entitlements

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { t := t0.Add(d); return &t }

func TestComputeThePlanFromTheRows(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		name      string
		rows      []Row
		pro       bool
		source    string
		end       *time.Time
		trialEnds *time.Time
	}{
		{"no rows is free", nil, false, "", nil, nil},
		{"an active subscription until its end", []Row{{SourceStripe, StatusActive, at(10 * day)}}, true, SourceStripe, at(10 * day), nil},
		{"a cancelled subscription is active until its end, then free", []Row{{SourceApple, StatusActive, at(-time.Second)}}, false, "", nil, nil},
		{"active with no end (a grant) never lapses", []Row{{SourceGrant, StatusActive, nil}}, true, SourceGrant, nil, nil},
		{"past due keeps Pro through the 7-day grace", []Row{{SourceStripe, StatusPastDue, at(-6 * day)}}, true, SourceStripe, at(day), nil},
		{"past due after the grace is free", []Row{{SourceStripe, StatusPastDue, at(-8 * day)}}, false, "", nil, nil},
		{"past due with no end is malformed: nothing", []Row{{SourceStripe, StatusPastDue, nil}}, false, "", nil, nil},
		{"an ended row never grants, whatever its date", []Row{{SourceStripe, StatusEnded, at(30 * day)}}, false, "", nil, nil},
		{"a running trial reports its end", []Row{{SourceTrial, StatusActive, at(5 * day)}}, true, SourceTrial, at(5 * day), at(5 * day)},
		{"an over trial is free", []Row{{SourceTrial, StatusActive, at(-day)}}, false, "", nil, nil},
		{"founding year", []Row{{SourceFounding, StatusActive, at(300 * day)}}, true, SourceFounding, at(300 * day), nil},
		{"a subscription outranks a running trial and hides its countdown", []Row{
			{SourceTrial, StatusActive, at(5 * day)}, {SourceStripe, StatusActive, at(20 * day)}}, true, SourceStripe, at(20 * day), nil},
		{"founding outranks a trial, a grant outranks a trial", []Row{
			{SourceTrial, StatusActive, at(5 * day)}, {SourceFounding, StatusActive, at(300 * day)}, {SourceGrant, StatusActive, at(50 * day)}}, true, SourceFounding, at(300 * day), nil},
		{"an ended subscription falls back to the running trial", []Row{
			{SourceStripe, StatusEnded, at(20 * day)}, {SourceTrial, StatusActive, at(5 * day)}}, true, SourceTrial, at(5 * day), at(5 * day)},
		{"apple and stripe tie: the later end is reported", []Row{
			{SourceApple, StatusActive, at(10 * day)}, {SourceStripe, StatusActive, at(40 * day)}}, true, SourceStripe, at(40 * day), nil},
		{"no end beats an end among equals", []Row{
			{SourceGrant, StatusActive, at(10 * day)}, {SourceGrant, StatusActive, nil}}, true, SourceGrant, nil, nil},
	} {
		got := Compute(c.rows, t0)
		if got.Pro != c.pro || got.Source != c.source || !sameTime(got.PeriodEnd, c.end) || !sameTime(got.TrialEnds, c.trialEnds) {
			t.Errorf("%s: %+v, want pro=%v source=%q end=%v trial=%v", c.name, got, c.pro, c.source, c.end, c.trialEnds)
		}
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

type failingConnector struct{}

func (failingConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("no database")
}
func (failingConnector) Driver() driver.Driver { return nil }

func TestBillingOffMakesEveryoneProWithoutReadingAnything(t *testing.T) {
	db := sql.OpenDB(failingConnector{}) // any read would fail
	defer db.Close()
	for name, s := range map[string]*Service{
		"billing off":        New(db, Config{Billing: false}),
		"no database at all": New(nil, Config{Billing: true}),
		"a nil service":      nil,
	} {
		st, err := s.State(context.Background(), uuid.New())
		if err != nil || !st.Pro || st.Source != SourceBillingOff || st.PeriodEnd != nil {
			t.Errorf("%s: %+v %v, want Pro with source billing_off and no end", name, st, err)
		}
		if s.Billing() {
			t.Errorf("%s: Billing() = true", name)
		}
		if !s.Pro(context.Background(), uuid.New()) {
			t.Errorf("%s: Pro() = false", name)
		}
	}
}

func TestDemoIsAlwaysProAndALookupFailureFailsOpen(t *testing.T) {
	db := sql.OpenDB(failingConnector{})
	defer db.Close()
	demo := uuid.New()
	s := New(db, Config{Billing: true, Demo: demo})
	if st, err := s.State(context.Background(), demo); err != nil || !st.Pro || st.Source != SourceDemo {
		t.Errorf("demo: %+v %v", st, err)
	}
	other := uuid.New()
	if _, err := s.State(context.Background(), other); err == nil {
		t.Error("a failed lookup returned no error")
	}
	// Fail open: a paying user must not lose sight of their own history because the lookup blipped.
	if !s.Pro(context.Background(), other) {
		t.Error("Pro() failed closed on a lookup error")
	}
	if !s.Billing() {
		t.Error("Billing() = false with billing on")
	}
}
