package entitlements

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParsePlannerProFrom(t *testing.T) {
	if got, err := ParsePlannerProFrom(" "); err != nil || !got.IsZero() {
		t.Errorf("unset: %v %v", got, err)
	}
	if got, err := ParsePlannerProFrom("2026-11-30T00:00:00+01:00"); err != nil || !got.Equal(time.Date(2026, 11, 29, 23, 0, 0, 0, time.UTC)) {
		t.Errorf("RFC 3339: %v %v", got, err)
	}
	if _, err := ParsePlannerProFrom("tomorrow"); err == nil {
		t.Error("a bad value was accepted")
	}
}

// Trip planning is Pro only for a free account created at or after the cutoff: grandfathered, Pro, demo and no-cutoff accounts keep it.
func TestPlannerOpen(t *testing.T) {
	cut := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	demo := uuid.New()
	mk := func(from time.Time, billing bool) *Service {
		db := sql.OpenDB(failingConnector{})
		t.Cleanup(func() { db.Close() })
		return New(db, Config{Billing: billing, Demo: demo, PlannerProFrom: from})
	}
	seed := func(s *Service, created time.Time, rows Rowset) uuid.UUID {
		id := uuid.New()
		s.cache[id] = cached{st: rows, created: created, at: s.now()}
		return id
	}
	pro := Rowset{{Source: SourceStripe, Status: StatusActive, PeriodEnd: at(30 * 24 * time.Hour)}}
	ctx := context.Background()
	s := mk(cut, true)
	before, after := cut.Add(-time.Hour), cut.Add(time.Hour)
	for name, tc := range map[string]struct {
		id   uuid.UUID
		want bool
	}{
		"grandfathered free":    {seed(s, before, nil), true},
		"new free":              {seed(s, after, nil), false},
		"created at the cutoff": {seed(s, cut, nil), false},
		"new Pro":               {seed(s, after, pro), true},
		"unknown creation":      {seed(s, time.Time{}, nil), true},
		"demo":                  {demo, true},
		"failed lookup":         {uuid.New(), true},
	} {
		if got := s.PlannerOpen(ctx, tc.id); got != tc.want {
			t.Errorf("%s: PlannerOpen = %v, want %v", name, got, tc.want)
		}
	}
	// No cutoff: the gate is off for everyone. Billing off: every account is Pro.
	off := mk(time.Time{}, true)
	if !off.PlannerOpen(ctx, seed(off, after, nil)) {
		t.Error("gate on with no cutoff")
	}
	nb := mk(cut, false)
	if !nb.PlannerOpen(ctx, uuid.New()) {
		t.Error("gate on with billing off")
	}
	var nilSvc *Service
	if !nilSvc.PlannerOpen(ctx, uuid.New()) {
		t.Error("nil service closed")
	}
}
