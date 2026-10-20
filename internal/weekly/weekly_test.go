package weekly

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/statement"
)

func TestWeekIsThePreviousLocalWeek(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	for _, tc := range []struct {
		now        time.Time
		key        string
		start, end string
	}{
		// Monday 2026-10-19 08:00 PDT: the week of Oct 12-18 is ISO week 42.
		{time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC), "weekly:2026-W42", "2026-10-12T07:00:00Z", "2026-10-19T07:00:00Z"},
		// A Sunday night still belongs to the week before.
		{time.Date(2026, 10, 19, 5, 0, 0, 0, time.UTC), "weekly:2026-W41", "2026-10-05T07:00:00Z", "2026-10-12T07:00:00Z"},
		// Across the DST change (Nov 1, 2026): the local days are 24 h wide, the UTC offset moves.
		{time.Date(2026, 11, 2, 16, 30, 0, 0, time.UTC), "weekly:2026-W44", "2026-10-26T07:00:00Z", "2026-11-02T08:00:00Z"},
		// ISO year boundary: the first Monday of 2027 closes ISO week 53 of 2026.
		{time.Date(2027, 1, 4, 16, 0, 0, 0, time.UTC), "weekly:2026-W53", "", ""},
	} {
		key, s, e := Week(tc.now, la)
		if key != tc.key {
			t.Errorf("%v: key %s, want %s", tc.now, key, tc.key)
		}
		if tc.end != "" && (s.Format(time.RFC3339) != tc.start || e.Format(time.RFC3339) != tc.end) {
			t.Errorf("%v: window %s .. %s, want %s .. %s", tc.now, s.Format(time.RFC3339), e.Format(time.RFC3339), tc.start, tc.end)
		}
	}
	// Due is Monday 08:00-11:59 local only.
	for _, tc := range []struct {
		now  time.Time
		want bool
	}{
		{time.Date(2026, 10, 19, 14, 59, 0, 0, time.UTC), false}, // 07:59 PDT
		{time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC), true},   // 08:00
		{time.Date(2026, 10, 19, 18, 59, 0, 0, time.UTC), true},  // 11:59
		{time.Date(2026, 10, 19, 19, 0, 0, 0, time.UTC), false},  // 12:00
		{time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC), false},  // Tuesday
	} {
		if got := Due(tc.now, la); got != tc.want {
			t.Errorf("Due(%v) = %v", tc.now, got)
		}
	}
}

func TestRender(t *testing.T) {
	home := 82
	s := Summary{Model: "R1S", Currency: "USD", Miles: 312.46, Drives: 9, KWhUsed: 118.2, MiPerKWh: 2.64, KWhCharged: 131.5, Cost: 24.18, HomePct: &home,
		BestMiles: 84.1, BestMiPerKWh: 3.1, Lease: &LeasePace{MilesOver: 41.2}}
	subj, text := Render(s, "https://rivolt.dev/?src=weekly", "https://rivolt.dev/api/weekly-recap/unsubscribe?t=x")
	if subj != "Your Rivian last week: 312.5 mi, $24.18" {
		t.Errorf("subject %q", subj)
	}
	for _, want := range []string{
		"Last week in your R1S:", "312.5 mi (9 drives)", "118.2 kWh, 2.64 mi/kWh", "131.5 kWh, $24.18 (82% at home)",
		"84.1 mi at 3.10 mi/kWh", "41 mi over pace for your allowance", "Open Rivolt: https://rivolt.dev/?src=weekly",
		"Stop the weekly recap: https://rivolt.dev/api/weekly-recap/unsubscribe?t=x", "unofficial and not affiliated with Rivian",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// The optional rows drop out.
	s2 := Summary{Currency: "EUR", Miles: 40, Drives: 1, KWhCharged: 10, Cost: 3}
	subj2, text2 := Render(s2, "u", "v")
	for _, gone := range []string{"Energy used", "Best drive", "Lease pace", "at home"} {
		if strings.Contains(text2, gone) {
			t.Errorf("row %q should be omitted:\n%s", gone, text2)
		}
	}
	if subj2 != "Your Rivian last week: 40.0 mi, 3.00 EUR" || !strings.Contains(text2, "1 drive)") || !strings.Contains(text2, "Last week in your Rivian:") {
		t.Errorf("minimal: %q\n%s", subj2, text2)
	}
	_, under := Render(Summary{Miles: 1, Drives: 1, Lease: &LeasePace{MilesOver: -120.4}}, "u", "v")
	if !strings.Contains(under, "120 mi under pace") {
		t.Errorf("under pace: %s", under)
	}
	// No tracking: no images, no pixel.
	if strings.Contains(strings.ToLower(text), "<img") || strings.Contains(text, "utm_") {
		t.Error("tracking in the recap")
	}
}

type fakeSrc struct {
	cands   []Candidate
	enabled bool
}

func (f *fakeSrc) Candidates(context.Context) ([]Candidate, error)  { return f.cands, nil }
func (f *fakeSrc) Enabled(context.Context, uuid.UUID) (bool, error) { return f.enabled, nil }

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

type claims struct {
	mu   sync.Mutex
	rows map[string]string
}

func (c *claims) Timezone(context.Context, uuid.UUID) (string, bool, bool, error) {
	return "UTC", false, true, nil
}
func (c *claims) BudgetUsed(context.Context, uuid.UUID, time.Time) (int, error) { return 0, nil }
func (c *claims) Claim(_ context.Context, m engage.Msg, _, _ time.Time, _ int) (bool, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := m.Kind + "|" + m.Key
	if _, ok := c.rows[k]; ok {
		return false, false, nil
	}
	c.rows[k] = "sending"
	return true, false, nil
}
func (c *claims) Mark(_ context.Context, _ uuid.UUID, kind, key, status string, _ time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[kind+"|"+key] = status
	return nil
}
func (c *claims) RecordSkip(_ context.Context, _ uuid.UUID, kind, key string, _ engage.Channel, status string, _ time.Time) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := kind + "|" + key
	if _, ok := c.rows[k]; ok {
		return false, nil
	}
	c.rows[k] = status
	return true, nil
}
func (c *claims) StuckSending(context.Context, time.Duration, time.Time) (int, error) { return 0, nil }
func (c *claims) Purge(context.Context, time.Time) (int64, error)                     { return 0, nil }

func newJob(sum Summary, now time.Time) (*Job, *fakeMailer, *claims, *fakeSrc) {
	uid := uuid.New()
	src := &fakeSrc{enabled: true, cands: []Candidate{{User: uid, Email: "a@x.test", Timezone: "America/Los_Angeles", Model: "R1T"}}}
	ml := &fakeMailer{}
	cl := &claims{rows: map[string]string{}}
	j := &Job{
		Source: src, Mailer: ml, Signer: statement.NewScopedSigner([]byte("a-long-enough-secret-for-tests-1234"), "weekly"),
		BaseURL: "https://rivolt.dev/", Now: func() time.Time { return now },
		Engage: &engage.Engage{Store: cl, Now: func() time.Time { return now }},
		Pause:  func(context.Context) {},
		Summarize: func(context.Context, Candidate, time.Time, time.Time, *time.Location) (Summary, error) {
			return sum, nil
		},
	}
	return j, ml, cl, src
}

var mondayMorning = time.Date(2026, 10, 19, 15, 30, 0, 0, time.UTC) // 08:30 PDT

func TestSweepSendsOncePerWeek(t *testing.T) {
	j, ml, cl, _ := newJob(Summary{Miles: 100, Drives: 3, KWhCharged: 40, Cost: 8, Charges: 2}, mondayMorning)
	st, err := j.Sweep(context.Background())
	if err != nil || st.Sent != 1 || len(ml.sent) != 1 {
		t.Fatalf("stats %+v err %v sent %d", st, err, len(ml.sent))
	}
	m := ml.sent[0]
	if m.To != "a@x.test" || !strings.HasPrefix(m.Subject, "Your Rivian last week:") || m.IdempotencyKey == "" ||
		!strings.HasPrefix(m.Headers["List-Unsubscribe"], "<https://rivolt.dev/api/weekly-recap/unsubscribe?t=") ||
		m.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || !strings.Contains(m.Text, "R1T") {
		t.Errorf("message %+v", m)
	}
	if cl.rows["weekly_recap|weekly:2026-W42"] != "sent" {
		t.Errorf("claims %v", cl.rows)
	}
	// The next sweep in the same morning, and an hour later: nothing more.
	for _, d := range []time.Duration{30 * time.Minute, 90 * time.Minute} {
		j.Now = func() time.Time { return mondayMorning.Add(d) }
		j.Engage.Now = j.Now
		if st, _ := j.Sweep(context.Background()); st.Sent != 0 || len(ml.sent) != 1 {
			t.Errorf("sent again after %v: %+v", d, st)
		}
	}
}

func TestSweepSkipsEmptyWeeksAndOffTimes(t *testing.T) {
	// An empty week: no mail, and the week is closed (a late sweep cannot send it).
	j, ml, cl, _ := newJob(Summary{}, mondayMorning)
	st, _ := j.Sweep(context.Background())
	if st.Sent != 0 || st.Skipped != 1 || len(ml.sent) != 0 || cl.rows["weekly_recap|weekly:2026-W42"] != "skipped_empty" {
		t.Errorf("empty week: %+v rows %v", st, cl.rows)
	}
	// Not Monday morning: nothing is even computed.
	computed := 0
	for _, now := range []time.Time{mondayMorning.Add(-2 * time.Hour), mondayMorning.Add(24 * time.Hour), mondayMorning.Add(5 * time.Hour)} {
		j, ml, _, _ := newJob(Summary{Miles: 1, Drives: 1}, now)
		j.Summarize = func(context.Context, Candidate, time.Time, time.Time, *time.Location) (Summary, error) {
			computed++
			return Summary{Miles: 1, Drives: 1}, nil
		}
		_, _ = j.Sweep(context.Background())
		if computed != 0 || len(ml.sent) != 0 {
			t.Errorf("%v: computed %d sent %d", now, computed, len(ml.sent))
		}
	}
	// The toggle is read at send time.
	j, ml, _, src := newJob(Summary{Miles: 1, Drives: 1}, mondayMorning)
	src.enabled = false
	if st, _ := j.Sweep(context.Background()); st.Sent != 0 || len(ml.sent) != 0 {
		t.Errorf("sent to an account that turned it off: %+v", st)
	}
}

func TestFailuresAreNotRetriedAndFiguresNeverGuessed(t *testing.T) {
	j, ml, cl, _ := newJob(Summary{Miles: 1, Drives: 1}, mondayMorning)
	ml.err = errors.New("resend down")
	if st, _ := j.Sweep(context.Background()); st.Failed != 1 || cl.rows["weekly_recap|weekly:2026-W42"] != "failed" {
		t.Fatalf("rows %v stats %+v", cl.rows, st)
	}
	ml.err = nil
	j.Now = func() time.Time { return mondayMorning.Add(30 * time.Minute) }
	j.Engage.Now = j.Now
	if st, _ := j.Sweep(context.Background()); st.Sent != 0 || len(ml.sent) != 0 {
		t.Errorf("a failed week was retried: %+v", st)
	}
	// A ledger error sends nothing and claims nothing.
	j2, ml2, cl2, _ := newJob(Summary{}, mondayMorning)
	j2.Summarize = func(context.Context, Candidate, time.Time, time.Time, *time.Location) (Summary, error) {
		return Summary{}, errors.New("ledger down")
	}
	if st, _ := j2.Sweep(context.Background()); st.Failed != 1 || len(ml2.sent) != 0 || len(cl2.rows) != 0 {
		t.Errorf("ledger error: %+v rows %v", st, cl2.rows)
	}
}

func TestScopedSignerKeepsKindsApart(t *testing.T) {
	secret := []byte("a-long-enough-secret-for-tests-1234")
	weeklyS, stmtS := statement.NewScopedSigner(secret, "weekly"), statement.NewSigner(secret)
	uid := uuid.New()
	tok := weeklyS.Sign(uid, "weekly:2026-W42", time.Now().Add(time.Hour))
	if _, _, err := stmtS.Verify(tok, time.Now()); err == nil {
		t.Error("a weekly token verified as a statement token")
	}
	if _, _, err := weeklyS.Verify(stmtS.Sign(uid, "2026-09", time.Now().Add(time.Hour)), time.Now()); err == nil {
		t.Error("a statement token verified as a weekly token")
	}
	if got, key, err := weeklyS.Verify(tok, time.Now()); err != nil || got != uid || key != "weekly:2026-W42" {
		t.Errorf("own token: %v %q %v", got, key, err)
	}
}
