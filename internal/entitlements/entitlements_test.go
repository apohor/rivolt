package entitlements

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
		{"an active subscription until its end", []Row{{Source: SourceStripe, Status: StatusActive, PeriodEnd: at(10 * day)}}, true, SourceStripe, at(10 * day), nil},
		{"a cancelled subscription is active until its end, then free", []Row{{Source: SourceApple, Status: StatusActive, PeriodEnd: at(-time.Second)}}, false, "", nil, nil},
		{"active with no end (a grant) never lapses", []Row{{Source: SourceGrant, Status: StatusActive, PeriodEnd: nil}}, true, SourceGrant, nil, nil},
		{"past due keeps Pro through the 7-day grace", []Row{{Source: SourceStripe, Status: StatusPastDue, PeriodEnd: at(-6 * day)}}, true, SourceStripe, at(day), nil},
		{"past due after the grace is free", []Row{{Source: SourceStripe, Status: StatusPastDue, PeriodEnd: at(-8 * day)}}, false, "", nil, nil},
		{"past due with no end is malformed: nothing", []Row{{Source: SourceStripe, Status: StatusPastDue, PeriodEnd: nil}}, false, "", nil, nil},
		{"an ended row never grants, whatever its date", []Row{{Source: SourceStripe, Status: StatusEnded, PeriodEnd: at(30 * day)}}, false, "", nil, nil},
		{"a running trial reports its end", []Row{{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)}}, true, SourceTrial, at(5 * day), at(5 * day)},
		{"an over trial is free", []Row{{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(-day)}}, false, "", nil, nil},
		{"founding year", []Row{{Source: SourceFounding, Status: StatusActive, PeriodEnd: at(300 * day)}}, true, SourceFounding, at(300 * day), nil},
		{"a subscription outranks a running trial and hides its countdown", []Row{
			{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)}, {Source: SourceStripe, Status: StatusActive, PeriodEnd: at(20 * day)}}, true, SourceStripe, at(20 * day), nil},
		{"founding outranks a trial, a grant outranks a trial", []Row{
			{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)}, {Source: SourceFounding, Status: StatusActive, PeriodEnd: at(300 * day)}, {Source: SourceGrant, Status: StatusActive, PeriodEnd: at(50 * day)}}, true, SourceFounding, at(300 * day), nil},
		{"an ended subscription falls back to the running trial", []Row{
			{Source: SourceStripe, Status: StatusEnded, PeriodEnd: at(20 * day)}, {Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)}}, true, SourceTrial, at(5 * day), at(5 * day)},
		{"apple and stripe tie: the later end is reported", []Row{
			{Source: SourceApple, Status: StatusActive, PeriodEnd: at(10 * day)}, {Source: SourceStripe, Status: StatusActive, PeriodEnd: at(40 * day)}}, true, SourceStripe, at(40 * day), nil},
		{"no end beats an end among equals", []Row{
			{Source: SourceGrant, Status: StatusActive, PeriodEnd: at(10 * day)}, {Source: SourceGrant, Status: StatusActive, PeriodEnd: nil}}, true, SourceGrant, nil, nil},
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

func TestLapseIsReportedOnAFreeAccountThatOnceHadPro(t *testing.T) {
	day := 24 * time.Hour
	upd := func(d time.Duration) time.Time { return t0.Add(d) }
	cases := []struct {
		name   string
		rows   []Row
		ended  *time.Time
		source string
	}{
		{"never had Pro", nil, nil, ""},
		{"a subscription that ran out", []Row{{Source: SourceStripe, Status: StatusActive, PeriodEnd: at(-3 * day)}}, at(-3 * day), SourceStripe},
		{"a trial that ran out", []Row{{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(-day)}}, at(-day), SourceTrial},
		{"past due after the grace: the end is the grace's end", []Row{{Source: SourceApple, Status: StatusPastDue, PeriodEnd: at(-10 * day)}}, at(-3 * day), SourceApple},
		{"refunded with a future period end: dated by the update", []Row{{Source: SourceStripe, Status: StatusEnded, PeriodEnd: at(20 * day), UpdatedAt: upd(-2 * day)}}, at(-2 * day), SourceStripe},
		{"ended after its period end: the period end", []Row{{Source: SourceStripe, Status: StatusEnded, PeriodEnd: at(-5 * day), UpdatedAt: upd(-day)}}, at(-5 * day), SourceStripe},
		{"the most recent of several", []Row{
			{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(-30 * day)}, {Source: SourceStripe, Status: StatusActive, PeriodEnd: at(-2 * day)}}, at(-2 * day), SourceStripe},
	}
	for _, c := range cases {
		got := Compute(c.rows, t0)
		if got.Pro || !sameTime(got.EndedAt, c.ended) || got.EndedSource != c.source {
			t.Errorf("%s: %+v, want free, ended %v from %q", c.name, got, c.ended, c.source)
		}
	}
	// A Pro account reports no lapse, even if an older grant ran out.
	got := Compute([]Row{{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(-day)}, {Source: SourceStripe, Status: StatusActive, PeriodEnd: at(day)}}, t0)
	if !got.Pro || got.EndedAt != nil || got.EndedSource != "" {
		t.Errorf("a Pro account carries a lapse: %+v", got)
	}
}

// A Stripe subscription bought during the trial must start billing when the trial ends: the checkout asks for the running trial's end,
// whatever else the account has.
func TestActiveTrialEnd(t *testing.T) {
	day := 24 * time.Hour
	if end, ok := ActiveTrialEnd([]Row{{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)}}, t0); !ok || !end.Equal(*at(5 * day)) {
		t.Errorf("running trial: %v %v", end, ok)
	}
	for name, rows := range map[string][]Row{
		"none":          nil,
		"trial over":    {{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(-day)}},
		"trial ended":   {{Source: SourceTrial, Status: StatusEnded, PeriodEnd: at(5 * day)}},
		"only founding": {{Source: SourceFounding, Status: StatusActive, PeriodEnd: at(300 * day)}},
	} {
		if _, ok := ActiveTrialEnd(rows, t0); ok {
			t.Errorf("%s: reported a running trial", name)
		}
	}
	// With a subscription already running the trial end is still the trial's own (the caller decides what to do with it).
	if _, ok := ActiveTrialEnd([]Row{{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)}, {Source: SourceStripe, Status: StatusActive, PeriodEnd: at(20 * day)}}, t0); !ok {
		t.Error("trial hidden by a subscription")
	}
}

// "Cancelled, paid until ..." and "payment failed, grace until ...": the two states a paid account shows besides plain Pro. Only a paid
// row that is the one granting reports them; the grant itself is unchanged (Pro until the period end, or end + grace).
func TestCancelAndGraceStates(t *testing.T) {
	day := 24 * time.Hour
	end := at(10 * day)
	for _, c := range []struct {
		name          string
		rows          []Row
		pro           bool
		cancels, graz *time.Time
	}{
		{"a stripe subscription set to not renew", []Row{{Source: SourceStripe, Status: StatusActive, PeriodEnd: end, CancelAtPeriodEnd: true}}, true, end, nil},
		{"an apple subscription set to not renew", []Row{{Source: SourceApple, Status: StatusActive, PeriodEnd: end, CancelAtPeriodEnd: true}}, true, end, nil},
		{"a renewing subscription reports neither", []Row{{Source: SourceStripe, Status: StatusActive, PeriodEnd: end}}, true, nil, nil},
		{"a failed renewal reports its grace end", []Row{{Source: SourceStripe, Status: StatusPastDue, PeriodEnd: at(3 * day)}}, true, nil, at(3*day + Grace)},
		{"a failed renewal past its grace is free and reports neither", []Row{{Source: SourceStripe, Status: StatusPastDue, PeriodEnd: at(-8 * day)}}, false, nil, nil},
		{"a cancelled subscription past its end is free", []Row{{Source: SourceStripe, Status: StatusActive, PeriodEnd: at(-time.Second), CancelAtPeriodEnd: true}}, false, nil, nil},
		{"an ended row reports neither", []Row{{Source: SourceStripe, Status: StatusEnded, PeriodEnd: end, CancelAtPeriodEnd: true}}, false, nil, nil},
		{"a trial cannot be cancelled", []Row{{Source: SourceTrial, Status: StatusActive, PeriodEnd: end, CancelAtPeriodEnd: true}}, true, nil, nil},
		{"only a paid row reports grace, whatever its status", []Row{{Source: SourceFounding, Status: StatusPastDue, PeriodEnd: at(3 * day)}}, true, nil, nil},
		{"the subscription wins over a trial, and so does its state", []Row{
			{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)},
			{Source: SourceStripe, Status: StatusActive, PeriodEnd: end, CancelAtPeriodEnd: true}}, true, end, nil},
		{"a lapsed subscription does not hide the trial that still grants", []Row{
			{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)},
			{Source: SourceStripe, Status: StatusPastDue, PeriodEnd: at(-8 * day)}}, true, nil, nil},
		{"billing's grace beats nothing else: a grace row and a founding grant report the paid state", []Row{
			{Source: SourceFounding, Status: StatusActive, PeriodEnd: at(100 * day)},
			{Source: SourceApple, Status: StatusPastDue, PeriodEnd: at(3 * day)}}, true, nil, at(3*day + Grace)},
	} {
		got := Compute(c.rows, t0)
		if got.Pro != c.pro || !sameTime(got.CancelsAt, c.cancels) || !sameTime(got.GraceEnds, c.graz) {
			t.Errorf("%s: pro=%v cancels=%v grace=%v, want pro=%v cancels=%v grace=%v", c.name, got.Pro, got.CancelsAt, got.GraceEnds, c.pro, c.cancels, c.graz)
		}
	}
}

// The free Pro a subscription must wait for: the latest end among a running trial, grant and the founding year, nothing for an ended or past row.
func TestActiveFreeEnd(t *testing.T) {
	day := 24 * time.Hour
	if _, ok := ActiveFreeEnd(nil, t0); ok {
		t.Error("no rows reads as free Pro")
	}
	if got, ok := ActiveFreeEnd([]Row{{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)}}, t0); !ok || !got.Equal(*at(5 * day)) {
		t.Errorf("a running trial: %v %v", got, ok)
	}
	rows := []Row{
		{Source: SourceTrial, Status: StatusActive, PeriodEnd: at(5 * day)},
		{Source: SourceFounding, Status: StatusActive, PeriodEnd: at(200 * day)},
	}
	if got, ok := ActiveFreeEnd(rows, t0); !ok || !got.Equal(*at(200 * day)) {
		t.Errorf("trial and founding: %v %v, want the founding end", got, ok)
	}
	// A grant with an end is free time a subscription waits for, as the founding year is.
	if got, ok := ActiveFreeEnd([]Row{{Source: SourceGrant, Status: StatusActive, PeriodEnd: at(30 * day)}}, t0); !ok || !got.Equal(*at(30 * day)) {
		t.Errorf("a running grant: %v %v", got, ok)
	}
	for name, r := range map[string]Row{
		"an ended trial":    {Source: SourceTrial, Status: StatusEnded, PeriodEnd: at(5 * day)},
		"a past founding":   {Source: SourceFounding, Status: StatusActive, PeriodEnd: at(-day)},
		"a paid row":        {Source: SourceStripe, Status: StatusActive, PeriodEnd: at(30 * day)},
		"an ended grant":    {Source: SourceGrant, Status: StatusEnded, PeriodEnd: at(30 * day)},
		"a row with no end": {Source: SourceTrial, Status: StatusActive},
	} {
		if _, ok := ActiveFreeEnd([]Row{r}, t0); ok {
			t.Errorf("%s reads as free Pro", name)
		}
	}
}

// Nothing writes the 'trial' source any more (a new account starts free): the source stays readable for rows that exist, but no
// production code inserts one. Test files and migrations are not production writers.
func TestNothingWritesTrialEntitlements(t *testing.T) {
	root := "../.."
	insert := regexp.MustCompile(`(?is)INSERT\s+INTO\s+entitlements[^;]*'trial'`)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || n == "migrations" || n == "web" || n == "ios" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if insert.Match(b) {
			t.Errorf("%s inserts a 'trial' entitlement", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
