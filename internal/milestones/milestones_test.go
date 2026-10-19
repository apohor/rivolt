package milestones

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/ledger"
)

var t0 = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

func drive(i int, miles, kwh float64, at time.Time) ledger.Drive {
	return ledger.Drive{ID: "d" + string(rune('a'+i%26)) + at.Format("0102150405"), VehicleID: "rv", StartedAt: at, EndedAt: at.Add(time.Hour), DistanceMi: f(miles), EnergyKWh: f(kwh), Source: "live"}
}

// home is the saved home place; a charge at it is a home charge.
var home = &ledger.LatLon{Lat: 34.16, Lon: -118.17}

func charge(i int, kwh float64, at time.Time, homeSite bool) ledger.Charge {
	lat, lon := 36.0, -115.0
	if homeSite {
		lat, lon = home.Lat, home.Lon
	}
	return ledger.Charge{ID: "c" + at.Format("0102150405") + string(rune('a'+i%26)), VehicleID: "rv", StartedAt: at, EndedAt: at.Add(2 * time.Hour),
		EnergyKWh: f(kwh), Cost: f(kwh * 0.2), Currency: "USD", Source: "live", FinalState: "charging_complete", Lat: f(lat), Lon: f(lon)}
}

func input(ds []ledger.Drive, cs []ledger.Charge) ledger.Input {
	return ledger.Input{Drives: ds, Charges: cs, Now: t0, Config: ledger.Config{Currency: "USD", HomeRate: 0.2, Home: home}}
}

func kinds(fs []Found) string {
	var out []string
	for _, x := range fs {
		out = append(out, x.Key)
	}
	return strings.Join(out, ",")
}

func TestEvaluateTotals(t *testing.T) {
	// 9 drives of 120 mi: 1,080 miles at 2.5 mi/kWh.
	var ds []ledger.Drive
	for i := 0; i < 9; i++ {
		ds = append(ds, drive(i, 120, 48, t0.Add(-time.Duration(30-i)*24*time.Hour)))
	}
	got := Evaluate(input(ds, nil), t0, LeaseTerm{}, "")
	if kinds(got) != KindMiles {
		t.Fatalf("got %s, want just miles_1000", kinds(got))
	}
	d := got[0].Detail
	if d.Miles != 1080 || d.MiPerKWh != 2.5 {
		t.Errorf("detail %+v", d)
	}
	// 999 miles is not there yet.
	if kinds(Evaluate(input(ds[:8], []ledger.Charge{drive0Charge()}), t0, LeaseTerm{}, "")) != "" {
		t.Error("960 miles announced")
	}
	// 100 charges (and 1 MWh at home when they are home sessions of 10+ kWh).
	var cs []ledger.Charge
	for i := 0; i < 100; i++ {
		cs = append(cs, charge(i, 10.5, t0.Add(-time.Duration(200-i)*24*time.Hour), true))
	}
	got = Evaluate(input(nil, cs), t0, LeaseTerm{}, "")
	if kinds(got) != KindHomeMWh+","+KindCharges {
		t.Errorf("100 home charges of 10.5 kWh: %s", kinds(got))
	}
	// Away charges do not count toward the home MWh.
	var away []ledger.Charge
	for i := 0; i < 100; i++ {
		away = append(away, charge(i, 12, t0.Add(-time.Duration(200-i)*24*time.Hour), false))
	}
	if got := Evaluate(input(nil, away), t0, LeaseTerm{}, ""); kinds(got) != KindCharges {
		t.Errorf("away charges: %s", kinds(got))
	}
}

func drive0Charge() ledger.Charge { return charge(0, 20, t0.Add(-48*time.Hour), true) }

func TestEvaluateLease(t *testing.T) {
	in := input(nil, nil)
	for name, tc := range map[string]struct {
		l    LeaseTerm
		want string
	}{
		"no lease":       {LeaseTerm{}, ""},
		"before half":    {LeaseTerm{Elapsed: 539, Total: 1095}, ""},
		"half":           {LeaseTerm{Elapsed: 548, Total: 1095}, KindLease},
		"well past half": {LeaseTerm{Elapsed: 900, Total: 1095}, KindLease},
	} {
		if got := kinds(Evaluate(in, t0, tc.l, "")); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestPersonalBestEfficiency(t *testing.T) {
	day := func(n int) time.Time { return t0.Add(-time.Duration(60-n) * 24 * time.Hour) }
	var ds []ledger.Drive
	for i := 0; i < 5; i++ {
		ds = append(ds, drive(i, 60, 25, day(i))) // 2.40 mi/kWh, five long drives
	}
	cur := drive(9, 80, 30, day(20)) // 2.67 mi/kWh: 11 % better
	in := input(append(append([]ledger.Drive{}, ds...), cur), nil)
	got := Evaluate(in, t0, LeaseTerm{}, cur.ID)
	if len(got) != 1 || got[0].Kind != KindBestEff || got[0].Key != KindBestEff+":"+cur.EndedAt.UTC().Format("2006-01") || got[0].Detail.DriveMiles != 80 || got[0].Detail.DriveMiKWh != 2.67 {
		t.Fatalf("got %+v", got)
	}
	// Not named (a charge close or the catch-up): never.
	if got := Evaluate(in, t0, LeaseTerm{}, ""); len(got) != 0 {
		t.Errorf("announced without a drive: %+v", got)
	}
	for name, mod := range map[string]func() ledger.Input{
		"only 4 earlier long drives": func() ledger.Input { return input(append(append([]ledger.Drive{}, ds[:4]...), cur), nil) },
		"a 4 % gain": func() ledger.Input {
			c := drive(9, 80, 32, day(20))
			cur = c
			return input(append(append([]ledger.Drive{}, ds...), c), nil)
		},
		"a drive under 50 miles": func() ledger.Input {
			c := drive(9, 45, 15, day(20))
			cur = c
			return input(append(append([]ledger.Drive{}, ds...), c), nil)
		},
		"no energy recorded": func() ledger.Input {
			c := drive(9, 80, 30, day(20))
			c.EnergyKWh = nil
			cur = c
			return input(append(append([]ledger.Drive{}, ds...), c), nil)
		},
	} {
		in := mod()
		if got := Evaluate(in, t0, LeaseTerm{}, cur.ID); len(got) != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestText(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		d     Detail
		title string
		body  string
	}{
		{KindMiles, Detail{Miles: 1080, MiPerKWh: 2.31, CostPerMile: 0.07, Currency: "USD"}, "1,000 miles recorded", "Your first 1,000 miles in Rivolt: 2.31 mi/kWh, $0.07/mi."},
		{KindMiles, Detail{MiPerKWh: 2.31}, "1,000 miles recorded", "Your first 1,000 miles in Rivolt: 2.31 mi/kWh."},
		{KindMiles, Detail{}, "1,000 miles recorded", "Your first 1,000 miles in Rivolt."},
		{KindHomeMWh, Detail{HomeKWh: 1000}, "1,000 kWh charged at home", "1,000 kWh charged at home since you connected."},
		{KindHomeMWh, Detail{HomeKWh: 1000, HomeCost: 142, Currency: "USD"}, "1,000 kWh charged at home", "1,000 kWh charged at home since you connected. About $142 at your home rate."},
		{KindCharges, Detail{Charges: 100, KWhAdded: 2850}, "100 charges", "100 charges recorded in Rivolt, 2,850 kWh added."},
		{KindCharges, Detail{Charges: 100}, "100 charges", "100 charges recorded in Rivolt."},
		{KindLease, Detail{}, "Halfway through your lease", "Halfway through the term. Open Rivolt to see your mileage pace."},
		{KindBestEff, Detail{DriveMiles: 120, DriveMiKWh: 2.85}, "Your most efficient drive yet", "2.85 mi/kWh over 120 mi, your best on a drive this long."},
		{"nope", Detail{}, "Milestone", "A new milestone in Rivolt."},
	} {
		title, body := Text(tc.kind, tc.d)
		if title != tc.title || body != tc.body {
			t.Errorf("%s: %q / %q, want %q / %q", tc.kind, title, body, tc.title, tc.body)
		}
		// Aggregates only, no cheering, no places.
		if strings.Contains(body, "!") || strings.Contains(strings.ToLower(body), "congrat") {
			t.Errorf("%s: %q", tc.kind, body)
		}
	}
	if thousands(2850) != "2,850" || thousands(999) != "999" || thousands(1234567) != "1,234,567" || thousands(0) != "0" {
		t.Error("thousands")
	}
	if !PushAllowed(0) || !PushAllowed(2) || PushAllowed(3) || PushAllowed(4) {
		t.Error("a milestone push must leave two budget slots: allowed at 0-2 used, not at 3 or 4")
	}
	_ = engage.MonthlyBudget
}

type fakeStore struct {
	mu        sync.Mutex
	ready     bool
	rows      map[string]Row
	veh       VehicleRef
	sent      map[string]bool // key -> its push went out (an engagement_sends row exists)
	dismissed map[string]bool
}

func (s *fakeStore) Ready(context.Context) (bool, error) { return s.ready, nil }
func (s *fakeStore) MarkReady(context.Context) error     { s.ready = true; return nil }
func (s *fakeStore) Insert(_ context.Context, r Row) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := r.Vehicle.String() + "|" + r.Key
	if _, ok := s.rows[k]; ok {
		return false, nil
	}
	s.rows[k] = r
	return true, nil
}
func (s *fakeStore) Vehicle(context.Context, uuid.UUID, string) (uuid.UUID, bool, error) {
	return s.veh.ID, true, nil
}
func (s *fakeStore) Vehicles(context.Context, time.Time) ([]VehicleRef, error) {
	return []VehicleRef{s.veh}, nil
}
func (s *fakeStore) PendingPush(_ context.Context, since time.Time) ([]Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Row
	for _, r := range s.rows {
		if r.Status == StatusAchieved && !s.dismissed[r.Key] && !s.sent[r.Key] && r.AchievedAt.After(since) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AchievedAt.Before(out[j].AchievedAt) })
	return out, nil
}

func newChecker(in ledger.Input) (*Checker, *fakeStore, *[]Row) {
	st := &fakeStore{rows: map[string]Row{}, sent: map[string]bool{}, dismissed: map[string]bool{}, veh: VehicleRef{User: uuid.New(), ID: uuid.New(), RivianID: "rv"}}
	var pushed []Row
	c := &Checker{
		Store: st, Now: func() time.Time { return t0 },
		Input: func(context.Context, uuid.UUID, string) (ledger.Input, error) { return in, nil },
		// Like engage: a delivered push is recorded as sent.
		Push: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, r Row) {
			pushed = append(pushed, r)
			st.sent[r.Key] = true
		},
	}
	return c, st, &pushed
}

func milesInput() ledger.Input {
	var ds []ledger.Drive
	for i := 0; i < 9; i++ {
		ds = append(ds, drive(i, 120, 48, t0.Add(-time.Duration(30-i)*24*time.Hour)))
	}
	return input(ds, nil)
}

func TestNothingIsAnnouncedBeforeTheBackfill(t *testing.T) {
	c, st, pushed := newChecker(milesInput())
	c.Check(context.Background(), st.veh.User, "rv", "")
	if len(st.rows) != 0 || len(*pushed) != 0 {
		t.Fatalf("announced before the backfill: rows %d pushed %d", len(st.rows), len(*pushed))
	}
	// The backfill marks what is passed, silently, and then opens the door.
	if err := c.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !st.ready || len(*pushed) != 0 {
		t.Fatalf("ready %v pushed %d", st.ready, len(*pushed))
	}
	for _, r := range st.rows {
		if r.Status != StatusBackfilled {
			t.Errorf("%s is %s", r.Key, r.Status)
		}
	}
	// A close afterwards does not announce the milestone that was already passed.
	c.Check(context.Background(), st.veh.User, "rv", "")
	if len(*pushed) != 0 {
		t.Errorf("a backfilled milestone was announced: %+v", *pushed)
	}
}

func TestANewMilestoneIsAnnouncedOnce(t *testing.T) {
	c, st, pushed := newChecker(input(nil, nil)) // nothing passed at the backfill
	st.ready = true
	c.Input = func(context.Context, uuid.UUID, string) (ledger.Input, error) { return milesInput(), nil } // then 1,080 miles
	c.Check(context.Background(), st.veh.User, "rv", "")
	c.Check(context.Background(), st.veh.User, "rv", "") // the next close, and the catch-up
	if err := c.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*pushed) != 1 || (*pushed)[0].Kind != KindMiles || (*pushed)[0].Status != StatusAchieved {
		t.Fatalf("pushed %+v", *pushed)
	}
	if len(st.rows) != 1 {
		t.Errorf("rows %d", len(st.rows))
	}
}

// After a big import or restore one pass can find several milestones at once:
// all become cards, only one is pushed. And a backfilled row keeps no detail.
func TestOnePushPerPassAndBackfillKeepsNoDetail(t *testing.T) {
	var cs []ledger.Charge
	for i := 0; i < 100; i++ {
		cs = append(cs, charge(i, 12, t0.Add(-time.Duration(200-i)*24*time.Hour), true))
	}
	var ds []ledger.Drive
	for i := 0; i < 9; i++ {
		ds = append(ds, drive(i, 120, 48, t0.Add(-time.Duration(30-i)*24*time.Hour)))
	}
	in := input(ds, cs) // 1,080 mi, 100 charges, 1.2 MWh at home: three at once
	c, st, pushed := newChecker(input(nil, nil))
	st.ready = true
	c.Input = func(context.Context, uuid.UUID, string) (ledger.Input, error) { return in, nil }
	c.Check(context.Background(), st.veh.User, "rv", "")
	if len(st.rows) != 3 || len(*pushed) != 1 {
		t.Fatalf("rows %d pushed %d, want 3 cards and 1 push", len(st.rows), len(*pushed))
	}
	for _, r := range st.rows {
		if r.Status != StatusAchieved || r.Detail == (Detail{}) {
			t.Errorf("card %s: %+v", r.Key, r)
		}
	}

	b, bs, _ := newChecker(in)
	if err := b.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(bs.rows) != 3 {
		t.Fatalf("backfilled rows %d", len(bs.rows))
	}
	for _, r := range bs.rows {
		if r.Status != StatusBackfilled || r.Detail != (Detail{}) {
			t.Errorf("backfilled row %s keeps detail: %+v", r.Key, r)
		}
	}
}

// A push refused at night (outside 09-20 local) is offered again by a later
// catch-up pass, inside the window; once sent it is never offered again; a
// dismissed card, a milestone older than 72 h, and a backfilled one are not.
func TestRefusedPushIsReofferedOnTheNextPass(t *testing.T) {
	c, st, _ := newChecker(input(nil, nil))
	st.ready = true
	night := time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC) // the milestone is reached at 02:00
	morning := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	clock := night
	c.Now = func() time.Time { return clock }
	c.Input = func(context.Context, uuid.UUID, string) (ledger.Input, error) { return milesInput(), nil }

	var offered []string
	inWindow := func() bool { h := clock.Hour(); return h >= 9 && h < 20 }
	// Push stands in for engage: it refuses outside 09-20 and records a send inside it.
	c.Push = func(_ context.Context, _ uuid.UUID, _ uuid.UUID, r Row) {
		offered = append(offered, clock.Format("15:04")+" "+r.Key)
		if inWindow() {
			st.sent[r.Key] = true
		}
	}
	c.Check(context.Background(), st.veh.User, "rv", "") // 02:00: achieved, push refused
	if len(offered) != 1 || len(st.sent) != 0 {
		t.Fatalf("at night: offered %v sent %v", offered, st.sent)
	}
	clock = night.Add(2 * time.Hour) // a catch-up at 04:00 is still outside the window: offered, refused again
	if err := c.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(offered) != 2 || len(st.sent) != 0 {
		t.Fatalf("04:00 pass: offered %v sent %v", offered, st.sent)
	}
	clock = morning // 10:00: goes out
	if err := c.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(offered) != 3 || !st.sent[KindMiles] {
		t.Fatalf("10:00 pass: offered %v sent %v", offered, st.sent)
	}
	clock = morning.Add(6 * time.Hour) // never again
	if err := c.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(offered) != 3 {
		t.Errorf("a sent milestone was offered again: %v", offered)
	}
}

func TestReofferBounds(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	mk := func(key, status string, age time.Duration) Row {
		return Row{User: uuid.New(), Vehicle: uuid.New(), Key: key, Kind: kindOfKey(key), Status: status, AchievedAt: now.Add(-age)}
	}
	c, st, pushed := newChecker(input(nil, nil))
	st.ready = true
	c.Now = func() time.Time { return now }
	rows := []Row{
		mk("miles_1000", StatusAchieved, 2*time.Hour),   // eligible
		mk("charges_100", StatusAchieved, 73*time.Hour), // too old: card only
		mk("home_mwh_1", StatusBackfilled, time.Hour),   // backfilled: never
		mk("lease_half", StatusAchieved, time.Hour),     // dismissed
	}
	for _, r := range rows {
		st.rows[r.Vehicle.String()+"|"+r.Key] = r
	}
	st.dismissed["lease_half"] = true
	if err := c.reoffer(context.Background(), map[uuid.UUID]bool{}); err != nil {
		t.Fatal(err)
	}
	if len(*pushed) != 1 || (*pushed)[0].Key != "miles_1000" {
		t.Fatalf("offered %+v, want only the 2-hour-old miles milestone", *pushed)
	}

	// At most one per account per pass: two eligible for one account, one goes.
	c2, st2, pushed2 := newChecker(input(nil, nil))
	st2.ready = true
	c2.Now = func() time.Time { return now }
	u, v := uuid.New(), uuid.New()
	for i, k := range []string{"miles_1000", "charges_100"} {
		st2.rows[v.String()+"|"+k] = Row{User: u, Vehicle: v, Key: k, Kind: k, Status: StatusAchieved, AchievedAt: now.Add(-time.Duration(i+1) * time.Hour)}
	}
	if err := c2.reoffer(context.Background(), map[uuid.UUID]bool{}); err != nil {
		t.Fatal(err)
	}
	if len(*pushed2) != 1 || (*pushed2)[0].Key != "charges_100" { // the older one first
		t.Errorf("one per account: %+v", *pushed2)
	}
	// An account that already got a push this pass gets none.
	c2, st2, pushed2 = newChecker(input(nil, nil))
	st2.ready = true
	c2.Now = func() time.Time { return now }
	st2.rows[v.String()+"|miles_1000"] = Row{User: u, Vehicle: v, Key: "miles_1000", Kind: "miles_1000", Status: StatusAchieved, AchievedAt: now.Add(-time.Hour)}
	if err := c2.reoffer(context.Background(), map[uuid.UUID]bool{u: true}); err != nil || len(*pushed2) != 0 {
		t.Errorf("pushed again in the same pass: %+v %v", *pushed2, err)
	}
}
