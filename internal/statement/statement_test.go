package statement

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/ledger"
)

// ---- fixtures ------------------------------------------------------

func fp(v float64) *float64 { return &v }
func sp(s string) *string   { return &s }

func monthly(period string) ledger.Monthly {
	return ledger.Monthly{
		Period: period, Currency: "USD",
		Spend: ledger.Spend{Total: 83.45, PricedSessions: 6, UnpricedSessions: 0,
			BySource: map[string]float64{"rivian": 40, "home_rate": 43.45}},
		Energy: ledger.Energy{
			Sessions: 6, KWhAdded: 412.3,
			ByKind: map[string]ledger.KindLine{"ac": {Sessions: 4, KWh: 290, Cost: 43.45}, "dcfc": {Sessions: 2, KWh: 122.3, Cost: 40}},
			ByPlaceKind: map[string]map[string]ledger.KindLine{
				"home":    {"ac": {Sessions: 4, KWh: 290, Cost: 43.45}, "dcfc": {}},
				"away":    {"ac": {}, "dcfc": {Sessions: 2, KWh: 122.3, Cost: 40}},
				"unknown": {"ac": {}, "dcfc": {}},
			},
		},
		Driving: ledger.Driving{Drives: 30, Miles: 1204.5, CostPerMile: fp(0.0693)},
		Receipts: []ledger.Receipt{
			{LocalDate: "2026-09-12", Kind: "dcfc", Place: "away", KWh: 61.2, Cost: fp(24.5), CostSource: "rivian", Network: sp("Electrify America")},
			{LocalDate: "2026-09-03", Kind: "ac", Place: "home", KWh: 40, Cost: fp(6.4), CostSource: "home_rate",
				Sessions: []ledger.ReceiptSession{{ChargeID: "live_x_c_1"}}},
			{LocalDate: "2026-09-20", Kind: "dcfc", Place: "away", KWh: 61.1, Cost: fp(15.5), CostSource: "network_estimate", Network: sp("EVgo")},
			{LocalDate: "2026-09-25", Kind: "ac", Place: "away", KWh: 20, Cost: nil, CostSource: "unknown"},
		},
	}
}

func baseInput() Input {
	return Input{
		Period: "2026-09", FirstName: "Sam", Currency: "USD", Month: monthly("2026-09"),
		Links: Links{Statement: "https://rivolt.dev/money?month=2026-09&src=statement",
			Share:       "https://rivolt.dev/money?month=2026-09&src=statement&share=1",
			Unpriced:    "https://rivolt.dev/charges?unpriced=1&src=statement",
			Settings:    "https://rivolt.dev/settings?tab=notifications",
			Unsubscribe: "https://rivolt.dev/api/statement-email/unsubscribe?t=abc"},
	}
}

// ---- render ----------------------------------------------------------

func TestRender_TotalsAreTheLedgers(t *testing.T) {
	in := baseInput()
	r := Render(in)
	if r.Silent {
		t.Fatal("silent")
	}
	if r.Subject != "Your Rivian cost for September: $83.45" {
		t.Fatalf("subject = %q", r.Subject)
	}
	for _, want := range []string{"You spent $83.45 on charging", "412 kWh · 1204 mi · $0.07/mi", "Home", "$43.45", "290 kWh",
		"Fast charging", "$40.00", "Biggest charges", "$24.50", "(billed)", "Fast charge · Electrify America",
		"(estimated)", "See the full statement: https://rivolt.dev/money?month=2026-09&src=statement", "Hi Sam,",
		"independent, open-source app and is not affiliated with Rivian"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("text missing %q\n%s", want, r.Text)
		}
	}
	// Top 3 by cost, unpriced receipt never listed.
	if strings.Contains(r.Text, "2026-09-25") {
		t.Errorf("an unpriced receipt is listed among the biggest charges")
	}
	if !strings.Contains(r.HTML, "<!doctype html>") || strings.Contains(r.HTML, "<img") || strings.Contains(r.HTML, "http://") {
		t.Errorf("html must be inline-only, no images or insecure links")
	}
}

func TestRender_UnpricedOnlyLeadsWithNotPricedYet(t *testing.T) {
	in := baseInput()
	in.Month.Spend = ledger.Spend{Total: 0, PricedSessions: 0, UnpricedSessions: 3, UnpricedKWh: 120, UnpricedHomeRateEstimate: fp(19.2)}
	in.Month.Receipts = nil
	in.Month.Driving.CostPerMile = nil
	r := Render(in)
	if r.Subject != "Your Rivian charging for September: not priced yet" {
		t.Fatalf("subject = %q", r.Subject)
	}
	if strings.Contains(r.Text, "$0.00") || strings.Contains(r.Subject, "$0") {
		t.Fatalf("an unpriced month must not read as $0.00:\n%s", r.Text)
	}
	for _, want := range []string{"not priced yet", "3 charges, 120 kWh", "They aren't in the total", "About $19.20 at your home rate", "Price them: https://rivolt.dev/charges?unpriced=1"} {
		if !strings.Contains(strings.ToLower(r.Text), strings.ToLower(want)) {
			t.Errorf("missing %q\n%s", want, r.Text)
		}
	}
}

func TestRender_HonestyLines(t *testing.T) {
	in := baseInput()
	in.Month.Spend.UnpricedSessions, in.Month.Spend.UnpricedKWh = 2, 33
	in.Month.Spend.EstimatedShare = fp(0.2)
	in.Month.DataQuality.CurrencyMismatchExcluded = 1
	in.Month.DataQuality.SupersededExcluded = 2
	r := Render(in)
	for _, want := range []string{"Not priced yet: 2 charges, 33 kWh", "estimated from public network rates",
		"1 charge used another currency and was left out", "We left out 2 duplicate records"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q\n%s", want, r.Text)
		}
	}
	// With unpriced sessions the month-over-month line is withheld.
	prev := monthly("2026-08")
	in.Prev = &prev
	if strings.Contains(Render(in).Text, "less than") || strings.Contains(Render(in).Text, "more than") {
		t.Error("delta shown although this month has unpriced sessions")
	}
}

// A month whose energy includes a charge estimated from the battery's change says so, in the text and the HTML, under the
// total; a month without one does not.
func TestRender_EstimatedEnergyIsSaid(t *testing.T) {
	const line = "estimated from the battery's change, because a Rivolt bug lost their energy count"
	in := baseInput()
	if r := Render(in); strings.Contains(r.Text, line) || strings.Contains(r.HTML, "battery") {
		t.Errorf("a month with no estimate says it has one:\n%s", r.Text)
	}
	in.Month.Energy.IncludesEstimated = true
	r := Render(in)
	if !strings.Contains(r.Text, line) {
		t.Errorf("text lacks the estimated-energy line:\n%s", r.Text)
	}
	// The apostrophe is escaped in HTML.
	if !strings.Contains(r.HTML, "estimated from the battery&#39;s change, because a Rivolt bug lost their energy count") {
		t.Errorf("html lacks the estimated-energy line:\n%s", r.HTML)
	}
	if strings.Count(r.Text, line) != 1 {
		t.Errorf("the line appears %d times in the text", strings.Count(r.Text, line))
	}
}

func TestRender_DeltaOnlyWhenBothMonthsFullyPriced(t *testing.T) {
	in := baseInput()
	prev := monthly("2026-08")
	prev.Spend.Total = 95.45
	in.Prev = &prev
	if r := Render(in); !strings.Contains(r.Text, "$12.00 less than August") {
		t.Fatalf("delta missing:\n%s", r.Text)
	}
	prev.Spend.UnpricedSessions = 1
	if r := Render(in); strings.Contains(r.Text, "than August") {
		t.Fatal("delta shown with an incomplete previous month")
	}
}

func TestRender_SilentMonthSendsNothing(t *testing.T) {
	in := baseInput()
	in.Month = ledger.Monthly{Period: "2026-09", Currency: "USD"}
	if r := Render(in); !r.Silent {
		t.Fatal("a month with no sessions and no drives must be silent")
	}
}

// Spec §9: no coordinates, addresses, VIN anywhere in the output.
func TestRender_NoCoordinatesAddressesOrVIN(t *testing.T) {
	in := baseInput()
	in.Month.Receipts[0].KMFromHome = fp(527.1) // present in the ledger, must not be rendered as a number of its own
	r := Render(in)
	all := r.Subject + "\n" + r.Text + "\n" + r.HTML
	for name, re := range map[string]*regexp.Regexp{
		"lat/lon pair":   regexp.MustCompile(`-?\d{1,3}\.\d{3,},\s*-?\d{1,3}\.\d{3,}`),
		"VIN":            regexp.MustCompile(`\b[A-HJ-NPR-Z0-9]{17}\b`),
		"street address": regexp.MustCompile(`(?i)\b\d{1,5}\s+\w+\s+(street|st|avenue|ave|road|rd|blvd|drive|dr|lane|ln)\b`),
		"km from home":   regexp.MustCompile(`527`),
		"charge id":      regexp.MustCompile(`live_[a-z0-9]+_c_`),
	} {
		if re.MatchString(all) {
			t.Errorf("%s leaked into the email", name)
		}
	}
}

func TestMoneyFormats(t *testing.T) {
	for _, c := range []struct {
		v    float64
		cur  string
		want string
	}{{83.4, "USD", "$83.40"}, {5, "EUR", "€5.00"}, {1.234, "GBP", "£1.23"}, {7, "CHF", "7.00 CHF"}, {-3, "USD", "-$3.00"}} {
		if got := Money(c.v, c.cur); got != c.want {
			t.Errorf("Money(%v,%s) = %s, want %s", c.v, c.cur, got, c.want)
		}
	}
}

// ---- tokens ----------------------------------------------------------

func TestToken_RoundTripTamperExpiryAndKey(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	uid := uuid.New()
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	tok := s.Sign(uid, "2026-09", now.Add(TokenTTL))
	gotU, gotP, err := s.Verify(tok, now)
	if err != nil || gotU != uid || gotP != "2026-09" {
		t.Fatalf("round trip: %v %v %v", gotU, gotP, err)
	}
	// expiry
	if _, _, err := s.Verify(tok, now.Add(TokenTTL+time.Second)); err == nil {
		t.Fatal("expired token accepted")
	}
	// tampering: payload and signature
	parts := strings.Split(tok, ".")
	for name, bad := range map[string]string{
		"flip payload": parts[0][:len(parts[0])-1] + "A" + "." + parts[1],
		"flip sig":     parts[0] + "." + parts[1][:len(parts[1])-1] + "A",
		"no dot":       parts[0] + parts[1],
		"empty":        "",
		"junk":         "!!!.???",
	} {
		if bad == tok {
			continue
		}
		if _, _, err := s.Verify(bad, now); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// a different key can't mint or verify
	other := NewSigner([]byte("ffffffffffffffffffffffffffffffff"))
	if _, _, err := other.Verify(tok, now); err == nil {
		t.Fatal("token verified under a different key")
	}
	// a token for another user can't be re-targeted: changing the uid
	// invalidates the signature.
	forged := strings.Replace(tok, parts[0], base64URL(uuid.New().String()+"|2026-09|"+itoa(now.Add(TokenTTL).Unix())), 1)
	if _, _, err := s.Verify(forged, now); err == nil {
		t.Fatal("forged payload accepted")
	}
}

// ---- schedule --------------------------------------------------------

func TestDue(t *testing.T) {
	chi, _ := time.LoadLocation("America/Chicago")
	cases := []struct {
		name string
		now  time.Time
		tz   string
		due  bool
		per  string
	}{
		{"1st 07:59 local: too early", time.Date(2026, 10, 1, 7, 59, 0, 0, chi), "America/Chicago", false, ""},
		{"1st 08:00 local: due for September", time.Date(2026, 10, 1, 8, 0, 0, 0, chi), "America/Chicago", true, "2026-09"},
		{"2nd 23:00 local: still due", time.Date(2026, 10, 2, 23, 0, 0, 0, chi), "America/Chicago", true, "2026-09"},
		{"3rd: window closed", time.Date(2026, 10, 3, 9, 0, 0, 0, chi), "America/Chicago", false, ""},
		{"mid month", time.Date(2026, 10, 15, 9, 0, 0, 0, chi), "America/Chicago", false, ""},
		{"January rolls the year back", time.Date(2027, 1, 1, 9, 0, 0, 0, time.UTC), "UTC", true, "2026-12"},
		{"UTC instant is still the previous day in Chicago", time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC), "America/Chicago", false, ""},
		{"bad zone falls back to UTC", time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), "Not/AZone", true, "2026-09"},
	}
	for _, c := range cases {
		per, _, due := Due(c.now, c.tz)
		if due != c.due || per != c.per {
			t.Errorf("%s: due=%v period=%q, want %v %q", c.name, due, per, c.due, c.per)
		}
	}
}

// ---- job -------------------------------------------------------------

type fakeStore struct {
	mu       sync.Mutex
	cands    []Candidate
	sends    map[string]*fakeSend
	prefs    map[uuid.UUID]Prefs
	emails   map[uuid.UUID]string
	stuck    int
	attempts map[string]int
}
type fakeSend struct {
	status  string
	created time.Time
	reason  string
}

func newFakeStore(cs ...Candidate) *fakeStore {
	return &fakeStore{cands: cs, sends: map[string]*fakeSend{}, prefs: map[uuid.UUID]Prefs{}, emails: map[uuid.UUID]string{}, attempts: map[string]int{}}
}
func key(uid uuid.UUID, p string) string { return uid.String() + "|" + p }
func (f *fakeStore) GetPrefs(_ context.Context, uid uuid.UUID) (Prefs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.prefs[uid]; ok {
		return p, nil
	}
	return Prefs{Timezone: "UTC"}, nil
}
func (f *fakeStore) SetPrefs(_ context.Context, uid uuid.UUID, p Prefs) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefs[uid] = p
	return nil
}
func (f *fakeStore) LastSentPeriod(context.Context, uuid.UUID) (string, error) { return "", nil }
func (f *fakeStore) Email(_ context.Context, uid uuid.UUID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.emails[uid], nil
}
func (f *fakeStore) OptedIn(context.Context) ([]Candidate, error) { return f.cands, nil }
func (f *fakeStore) Claim(_ context.Context, uid uuid.UUID, period string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(uid, period)
	if s, ok := f.sends[k]; ok {
		if s.status == "failed" && now.Sub(s.created) < RetryWindow && f.attempts[k] < MaxAttempts {
			s.status = "sending"
			f.attempts[k]++
			return true, nil
		}
		return false, nil
	}
	f.sends[k] = &fakeSend{status: "sending", created: now}
	f.attempts[k] = 1
	return true, nil
}
func (f *fakeStore) Mark(_ context.Context, uid uuid.UUID, period, status, reason string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.sends[key(uid, period)]; s != nil {
		s.status, s.reason = status, reason
	}
	return nil
}
func (f *fakeStore) StuckSending(context.Context, time.Duration, time.Time) (int, error) {
	return f.stuck, nil
}
func (f *fakeStore) Purge(context.Context, time.Time) (int64, error) { return 0, nil }

type fakeMailer struct {
	mu   sync.Mutex
	sent []email.Message
	err  error
}

func (m *fakeMailer) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

func newJob(st Store, mail Mailer, now time.Time) (*Job, *int) {
	pauses := 0
	return &Job{
		Store: st, Mailer: mail, Signer: NewSigner([]byte("0123456789abcdef0123456789abcdef")),
		BaseURL: "https://rivolt.dev/",
		Enabled: func(string) bool { return true },
		Monthly: func(_ context.Context, _ uuid.UUID, period string, _ *time.Location) (ledger.Monthly, error) {
			return monthly(period), nil
		},
		Now:   func() time.Time { return now },
		Pause: func(context.Context) { pauses++ },
	}, &pauses
}

var oct1 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func cand(tz string) Candidate {
	return Candidate{UserID: uuid.New(), Timezone: tz, Email: "u@example.com", FirstName: "Sam Rivera"}
}

func TestJob_SendsOnceWithUnsubscribeHeadersAndLinks(t *testing.T) {
	c := cand("UTC")
	st, mail := newFakeStore(c), &fakeMailer{}
	j, _ := newJob(st, mail, oct1)
	stats, err := j.RunOnce(context.Background())
	if err != nil || stats.Sent != 1 {
		t.Fatalf("stats %+v err %v", stats, err)
	}
	m := mail.sent[0]
	if m.To != "u@example.com" || !strings.Contains(m.Subject, "September") || !strings.Contains(m.Text, "Hi Sam,") {
		t.Fatalf("message: %+v", m)
	}
	if lu := m.Headers["List-Unsubscribe"]; !strings.HasPrefix(lu, "<https://rivolt.dev/api/statement-email/unsubscribe?t=") || !strings.HasSuffix(lu, ">") {
		t.Fatalf("List-Unsubscribe = %q", lu)
	}
	if m.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("one-click header: %v", m.Headers)
	}
	if !strings.Contains(m.Text, "https://rivolt.dev/money?month=2026-09&src=statement") || strings.Contains(m.Text, "rivolt.dev//") {
		t.Fatalf("links: %s", m.Text)
	}
	// Second run the same hour: nothing more.
	stats, _ = j.RunOnce(context.Background())
	if stats.Sent != 0 || stats.AlreadyDone != 1 || len(mail.sent) != 1 {
		t.Fatalf("second run sent again: %+v sent=%d", stats, len(mail.sent))
	}
}

func TestJob_BothFlagsAndTimingGate(t *testing.T) {
	c := cand("UTC")
	// Operator flag off: nothing, nothing claimed.
	st, mail := newFakeStore(c), &fakeMailer{}
	j, _ := newJob(st, mail, oct1)
	j.Enabled = func(string) bool { return false }
	if s, _ := j.RunOnce(context.Background()); s.Sent != 0 || len(st.sends) != 0 {
		t.Fatalf("operator flag off sent/claimed: %+v", s)
	}
	// Partial month / not due: the 15th.
	j2, _ := newJob(st, mail, time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC))
	if s, _ := j2.RunOnce(context.Background()); s.Sent != 0 || s.Due != 0 {
		t.Fatalf("sent mid-month: %+v", s)
	}
	// Not opted in: OptedIn() returns nobody.
	st3 := newFakeStore()
	j3, _ := newJob(st3, mail, oct1)
	if s, _ := j3.RunOnce(context.Background()); s.Considered != 0 || len(mail.sent) != 0 {
		t.Fatalf("sent without an opt-in: %+v", s)
	}
}

func TestJob_ConcurrentRunsNeverSendTwice(t *testing.T) {
	var cs []Candidate
	for i := 0; i < 20; i++ {
		cs = append(cs, cand("UTC"))
	}
	st, mail := newFakeStore(cs...), &fakeMailer{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, _ := newJob(st, mail, oct1) // a separate job per "replica"
			_, _ = j.RunOnce(context.Background())
		}()
	}
	wg.Wait()
	if len(mail.sent) != 20 {
		t.Fatalf("sent %d emails for 20 users across 4 concurrent runs, want exactly 20", len(mail.sent))
	}
}

func TestJob_SilentMonthSkippedLedgerErrorRetriedFailedRetriedWithinWindow(t *testing.T) {
	c := cand("UTC")
	st, mail := newFakeStore(c), &fakeMailer{}
	j, _ := newJob(st, mail, oct1)
	// Silent month: marked skipped, no mail, not retried.
	j.Monthly = func(_ context.Context, _ uuid.UUID, p string, _ *time.Location) (ledger.Monthly, error) {
		return ledger.Monthly{Period: p, Currency: "USD"}, nil
	}
	if s, _ := j.RunOnce(context.Background()); s.Skipped != 1 || len(mail.sent) != 0 {
		t.Fatalf("silent: %+v", s)
	}
	if s, _ := j.RunOnce(context.Background()); s.AlreadyDone != 1 {
		t.Fatalf("silent month retried: %+v", s)
	}

	// Ledger unreadable: nothing sent, retried next run.
	c2 := cand("UTC")
	st2, mail2 := newFakeStore(c2), &fakeMailer{}
	j2, _ := newJob(st2, mail2, oct1)
	broken := true
	j2.Monthly = func(_ context.Context, _ uuid.UUID, p string, _ *time.Location) (ledger.Monthly, error) {
		if broken {
			return ledger.Monthly{}, errors.New("db down")
		}
		return monthly(p), nil
	}
	if s, _ := j2.RunOnce(context.Background()); s.Failed != 1 || len(mail2.sent) != 0 {
		t.Fatalf("ledger error must not send: %+v", s)
	}
	broken = false
	if s, _ := j2.RunOnce(context.Background()); s.Sent != 1 {
		t.Fatalf("failed month not retried: %+v", s)
	}

	// Mailer failure retried within 3 days, abandoned after.
	c3 := cand("UTC")
	st3, mail3 := newFakeStore(c3), &fakeMailer{err: errors.New("resend 500")}
	now := oct1
	j3, _ := newJob(st3, mail3, now)
	j3.Now = func() time.Time { return now }
	if s, _ := j3.RunOnce(context.Background()); s.Failed != 1 {
		t.Fatalf("mailer failure: %+v", s)
	}
	mail3.err = nil
	now = oct1.Add(30 * time.Minute) // still the 1st
	if s, _ := j3.RunOnce(context.Background()); s.Sent != 1 {
		t.Fatalf("not retried within the window: %+v", s)
	}
}

func TestJob_ThrottlesBetweenSends(t *testing.T) {
	var cs []Candidate
	for i := 0; i < 3; i++ {
		cs = append(cs, cand("UTC"))
	}
	st, mail := newFakeStore(cs...), &fakeMailer{}
	j, pauses := newJob(st, mail, oct1)
	if s, _ := j.RunOnce(context.Background()); s.Sent != 3 {
		t.Fatalf("%+v", s)
	}
	if *pauses != 2 {
		t.Fatalf("pauses = %d, want 2 (between 3 sends)", *pauses)
	}
}

// Preview and send share Build, so they can't differ.
func TestJob_BuildIsWhatSendUses(t *testing.T) {
	c := cand("UTC")
	st, mail := newFakeStore(c), &fakeMailer{}
	j, _ := newJob(st, mail, oct1)
	cur := monthly("2026-09")
	prev := monthly("2026-08")
	built, ok := j.Build(c, "2026-09", cur, &prev)
	if !ok {
		t.Fatal("not buildable")
	}
	_, _ = j.RunOnce(context.Background())
	if len(mail.sent) != 1 || mail.sent[0].Subject != built.Subject || mail.sent[0].To != built.To {
		t.Fatalf("send differs from build: %q vs %q", mail.sent[0].Subject, built.Subject)
	}
}

func TestJob_AttemptsAreCappedAndStuckClaimsAreReported(t *testing.T) {
	c := cand("UTC")
	st, mail := newFakeStore(c), &fakeMailer{err: errors.New("resend 500")}
	st.stuck = 2
	j, _ := newJob(st, mail, oct1)
	var failed, claimedRuns int
	for i := 0; i < 6; i++ {
		s, _ := j.RunOnce(context.Background())
		if i == 0 && s.StuckSending != 2 {
			t.Fatalf("stuck = %d, want 2", s.StuckSending)
		}
		failed += s.Failed
		if s.Due > 0 && s.AlreadyDone == 0 {
			claimedRuns++
		}
	}
	if failed != MaxAttempts {
		t.Fatalf("a permanently failing send was attempted %d times, want %d", failed, MaxAttempts)
	}
}

func TestBuild_CarriesAStableOpaqueIdempotencyKey(t *testing.T) {
	c := cand("UTC")
	j, _ := newJob(newFakeStore(c), &fakeMailer{}, oct1)
	cur := monthly("2026-09")
	a, _ := j.Build(c, "2026-09", cur, nil)
	b, _ := j.Build(c, "2026-09", cur, nil)
	other, _ := j.Build(c, "2026-08", cur, nil)
	if a.IdempotencyKey == "" || a.IdempotencyKey != b.IdempotencyKey || a.IdempotencyKey == other.IdempotencyKey {
		t.Fatalf("keys: %q %q %q", a.IdempotencyKey, b.IdempotencyKey, other.IdempotencyKey)
	}
	if strings.Contains(a.IdempotencyKey, c.UserID.String()) {
		t.Fatal("the key exposes the user id")
	}
}

// The off-peak saving is one cost-only sentence, in the text and the HTML, with
// no link of its own; it is left out when there is no figure or it is small.
func TestRender_SavingsLine(t *testing.T) {
	in := baseInput()
	sav := ledger.Savings{Considered: 9, Moved: 6, MovedKWh: 183.4, CouldHaveSaved: fp(14.62)}
	in.Savings = &sav
	r := Render(in)
	const want = "Moving 183 kWh into your off-peak window would have saved about $15 (estimate)."
	if !strings.Contains(r.Text, want) || !strings.Contains(r.HTML, want) {
		t.Fatalf("savings line missing\n%s", r.Text)
	}
	for _, bad := range []string{"overpaid", "wasted", "tip", "Tip"} {
		if strings.Contains(r.Text, bad) {
			t.Errorf("savings wording has %q", bad)
		}
	}
	// It adds no link: the same links as without it.
	without := Render(baseInput())
	if strings.Count(r.Text, "https://") != strings.Count(without.Text, "https://") || strings.Count(r.HTML, "href=") != strings.Count(without.HTML, "href=") {
		t.Error("the savings line added a link")
	}
	for name, s := range map[string]ledger.Savings{
		"needs something": {MovedKWh: 183, CouldHaveSaved: fp(14), Needs: "more_home_sessions"},
		"no figure":       {MovedKWh: 183},
		"under a dollar":  {MovedKWh: 20, CouldHaveSaved: fp(0.8)},
		"under a kWh":     {MovedKWh: 0.4, CouldHaveSaved: fp(3)},
		"nothing to move": {MovedKWh: 0, CouldHaveSaved: fp(0)},
		"empty":           {},
	} {
		s := s
		in.Savings = &s
		if strings.Contains(Render(in).Text, "off-peak window") {
			t.Errorf("%s: a savings line was written", name)
		}
	}
	in.Savings = nil
	if strings.Contains(Render(in).Text, "off-peak window") {
		t.Error("a savings line without an estimate")
	}
}

// Fast charges priced from a typical public price read as estimated in the list.
func TestRender_ObservedEstimateIsLabelledEstimated(t *testing.T) {
	in := baseInput()
	in.Month.Receipts = append(in.Month.Receipts, ledger.Receipt{LocalDate: "2026-09-28", Kind: "dcfc", Place: "away", KWh: 50, Cost: fp(31.8),
		CostSource: ledger.SourceObservedEstimate, Network: sp("EVgo")})
	if r := Render(in); !strings.Contains(r.Text, "2026-09-28") || !strings.Contains(r.Text, "$31.80  (estimated)") && !strings.Contains(r.Text, "$31.80 (estimated)") {
		t.Errorf("observed estimate not labelled estimated:\n%s", r.Text)
	}
}

// The statement carries one plain iPhone-app line, in the text and the HTML, only when the link
// is given.
func TestRender_IOSAppLine(t *testing.T) {
	const link = "https://testflight.apple.com/join/AbCdEf12"
	in := baseInput()
	r := Render(in)
	if strings.Contains(r.Text, "iPhone") || strings.Contains(r.HTML, "iPhone") {
		t.Fatal("the line is there without a link")
	}
	in.Links.IOSApp = link
	r = Render(in)
	if !strings.Contains(r.Text, "Rivolt is also an iPhone app: "+link+"\n") {
		t.Errorf("text:\n%s", r.Text)
	}
	if !strings.Contains(r.HTML, `Rivolt is also an iPhone app: <a href="`+link+`">`+link+`</a>`) {
		t.Errorf("html:\n%s", r.HTML)
	}
}
