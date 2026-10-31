package r2arrival

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/statement"
)

type fakeStore struct {
	mu      sync.Mutex
	tz      string
	known   bool
	claimed map[string]bool
	used    int
}

func (f *fakeStore) Timezone(context.Context, uuid.UUID) (string, bool, bool, error) {
	return f.tz, f.known, f.known, nil
}
func (f *fakeStore) BudgetUsed(context.Context, uuid.UUID, time.Time) (int, error) {
	return f.used, nil
}
func (f *fakeStore) Claim(_ context.Context, m engage.Msg, _, _ time.Time, _ int) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := m.User.String() + m.Kind + m.Key
	if f.claimed[k] {
		return false, false, nil
	}
	f.claimed[k] = true
	return true, false, nil
}
func (f *fakeStore) Mark(context.Context, uuid.UUID, string, string, string, time.Time) error {
	return nil
}
func (f *fakeStore) RecordSkip(context.Context, uuid.UUID, string, string, engage.Channel, string, time.Time) (bool, error) {
	return true, nil
}
func (f *fakeStore) StuckSending(context.Context, time.Duration, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeStore) Purge(context.Context, time.Time) (int64, error) { return 0, nil }

type fakeSource struct {
	due        []db.R2ArrivalDue
	wanted     bool
	cleared    []uuid.UUID
	endedCalls int
}

func (s *fakeSource) Clear(_ context.Context, uid uuid.UUID, _ time.Time) error {
	s.cleared = append(s.cleared, uid)
	return nil
}
func (s *fakeSource) ClearEnded(context.Context, time.Time) (int64, error) {
	s.endedCalls++
	return 0, nil
}

func (s *fakeSource) Due(context.Context, time.Time, time.Time) ([]db.R2ArrivalDue, error) {
	return s.due, nil
}
func (s *fakeSource) StillWanted(context.Context, uuid.UUID, time.Time) (bool, error) {
	return s.wanted, nil
}

type fakeMail struct {
	mu   sync.Mutex
	sent []email.Message
}

func (m *fakeMail) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func TestSendTimeIsTheFirstWeekdayAt8(t *testing.T) {
	chi, _ := time.LoadLocation("America/Chicago")
	for _, c := range []struct {
		month time.Time
		want  string
	}{
		{time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "2026-11-02 08:00"}, // the 1st is a Sunday
		{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "2026-08-03 08:00"},  // a Saturday
		{time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), "2026-12-01 08:00"}, // a Tuesday
	} {
		if got := SendTime(c.month, chi).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("%v: %s, want %s", c.month.Format("2006-01"), got, c.want)
		}
	}
}

func job(t *testing.T, now time.Time, tz string, known bool, src *fakeSource) (*Job, *fakeMail, *fakeStore) {
	t.Helper()
	st := &fakeStore{tz: tz, known: known, claimed: map[string]bool{}}
	mail := &fakeMail{}
	signer := statement.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	return &Job{
		Source: src, Engage: &engage.Engage{Store: st, Now: func() time.Time { return now }}, Mail: mail,
		BaseURL: "https://rivolt.dev", Signer: func() *statement.Signer { return signer }, Now: func() time.Time { return now },
	}, mail, st
}

func TestSweepSendsOnceInTheMonthAndNotBeforeOrAfter(t *testing.T) {
	uid := uuid.New()
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{wanted: true, due: []db.R2ArrivalDue{{UserID: uid, Email: "a@example.test", Month: nov}}}
	chi := "America/Chicago"
	loc, _ := time.LoadLocation(chi)
	for _, c := range []struct {
		name string
		now  time.Time
		want int
	}{
		{"the evening before", time.Date(2026, 11, 1, 20, 0, 0, 0, loc), 0},
		{"Monday 07:59 local", time.Date(2026, 11, 2, 7, 59, 0, 0, loc), 0},
		{"Monday 08:00 local", time.Date(2026, 11, 2, 8, 0, 0, 0, loc), 1},
		{"late in the month", time.Date(2026, 11, 20, 12, 0, 0, 0, loc), 1},
		{"the month has passed", time.Date(2026, 12, 1, 9, 0, 0, 0, loc), 0},
	} {
		j, mail, _ := job(t, c.now, chi, true, src)
		j.Sweep(context.Background())
		if len(mail.sent) != c.want {
			t.Errorf("%s: %d sent, want %d", c.name, len(mail.sent), c.want)
		}
	}
	// Two sweeps, three replicas' worth: one send ever.
	j, mail, st := job(t, time.Date(2026, 11, 3, 9, 0, 0, 0, loc), chi, true, src)
	for i := 0; i < 3; i++ {
		j.Sweep(context.Background())
	}
	if len(mail.sent) != 1 {
		t.Fatalf("%d sent over repeated sweeps", len(mail.sent))
	}
	j2 := *j
	j2.Mail = mail
	j2.Sweep(context.Background()) // another replica sharing the claim store
	if len(mail.sent) != 1 || len(st.claimed) != 1 {
		t.Fatalf("a second replica sent again: %d", len(mail.sent))
	}
}

func TestSweepWithoutAZoneUsesTheFallbackHour(t *testing.T) {
	src := &fakeSource{wanted: true, due: []db.R2ArrivalDue{{UserID: uuid.New(), Email: "a@example.test", Month: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}}}
	j, mail, _ := job(t, time.Date(2026, 12, 1, 13, 59, 0, 0, time.UTC), "", false, src)
	j.Sweep(context.Background())
	if len(mail.sent) != 0 {
		t.Fatal("before 14:00 UTC with no zone")
	}
	j, mail, _ = job(t, time.Date(2026, 12, 1, 14, 0, 0, 0, time.UTC), "", false, src)
	j.Sweep(context.Background())
	if len(mail.sent) != 1 {
		t.Fatal("at 14:00 UTC with no zone")
	}
}

func TestSweepSkipsWhatNoLongerApplies(t *testing.T) {
	uid := uuid.New()
	src := &fakeSource{wanted: false, due: []db.R2ArrivalDue{{UserID: uid, Email: "a@example.test", Month: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}}}
	now := time.Date(2026, 12, 2, 15, 0, 0, 0, time.UTC)
	// Deleted, changed, connected a truck or turned emails off since the list was built: the send-time re-check refuses.
	j, mail, st := job(t, now, "UTC", true, src)
	j.Sweep(context.Background())
	if len(mail.sent) != 0 || len(st.claimed) != 0 {
		t.Fatal("sent although no longer wanted, or claimed it")
	}
	// Over the engagement budget: nothing sent, nothing claimed, tried again next sweep within the month.
	src.wanted = true
	j, mail, st = job(t, now, "UTC", true, src)
	st.used = engage.MonthlyBudget
	j.Sweep(context.Background())
	if len(mail.sent) != 0 || len(st.claimed) != 0 {
		t.Fatal("sent over the budget")
	}
	// No mailer, no signer, the demo account: nothing.
	j, mail, _ = job(t, now, "UTC", true, src)
	j.Signer = func() *statement.Signer { return nil }
	j.Sweep(context.Background())
	j2, mail2, _ := job(t, now, "UTC", true, src)
	j2.Skip = func(u uuid.UUID) bool { return u == uid }
	j2.Sweep(context.Background())
	if len(mail.sent)+len(mail2.sent) != 0 {
		t.Fatal("sent without a signer, or to a skipped user")
	}
	j3, _, _ := job(t, now, "UTC", true, src)
	j3.Mail = nil
	j3.Sweep(context.Background())
}

func TestMessageHasTheApprovedWordingAndAnUnsubscribe(t *testing.T) {
	m := BuildMessage("a@example.test", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), "https://rivolt.dev/", "TOK")
	if m.Subject != "Your R2 may be here: how to connect it to Rivolt" || m.To != "a@example.test" {
		t.Fatalf("%q to %q", m.Subject, m.To)
	}
	for _, want := range []string{
		"You asked for one note in December:", "Authorized Driver", "Settings → Account → Connect Rivian", "never stored", "it shows up in Activity once it ends",
		"recorded on the first R2s on Rivolt", "The Rivolt team", "unofficial and not affiliated with Rivian",
		"https://rivolt.dev/api/r2-arrival/unsubscribe?t=TOK",
	} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("text lacks %q", want)
		}
	}
	if m.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || !strings.Contains(m.Headers["List-Unsubscribe"], "TOK") {
		t.Errorf("headers: %v", m.Headers)
	}
	// A day or place of delivery never appears: the message only knows the month.
	for _, claim := range []string{"minutes", "two minutes", "within"} {
		if strings.Contains(m.Text+m.Subject, claim) {
			t.Errorf("the message makes a timing claim: %q", claim)
		}
	}
	if strings.Contains(m.Text, "2026") {
		t.Error("the message names a year")
	}
}

func TestTheMonthIsDeletedOnceItsEmailIsFinalAndEndedMonthsEveryTime(t *testing.T) {
	uid := uuid.New()
	loc, _ := time.LoadLocation("America/Chicago")
	src := &fakeSource{wanted: true, due: []db.R2ArrivalDue{{UserID: uid, Email: "a@example.test", Month: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}}}
	j, mail, _ := job(t, time.Date(2026, 11, 3, 9, 0, 0, 0, loc), "America/Chicago", true, src)
	j.Sweep(context.Background())
	if len(mail.sent) != 1 || len(src.cleared) != 1 || src.cleared[0] != uid || src.endedCalls != 1 {
		t.Fatalf("sent %d, cleared %v, ended-month sweeps %d", len(mail.sent), src.cleared, src.endedCalls)
	}
	// A second pass finds the claim and still deletes the month (a crash between claim and delete must not leave it).
	j.Sweep(context.Background())
	if len(mail.sent) != 1 || len(src.cleared) != 2 {
		t.Fatalf("already sent: sent %d, cleared %d", len(mail.sent), len(src.cleared))
	}
	// Over the budget, or no longer wanted: kept, to be tried again within the month.
	src2 := &fakeSource{wanted: true, due: src.due}
	j2, _, st := job(t, time.Date(2026, 11, 3, 9, 0, 0, 0, loc), "America/Chicago", true, src2)
	st.used = engage.MonthlyBudget
	j2.Sweep(context.Background())
	src2.wanted = false
	j3, _, _ := job(t, time.Date(2026, 11, 3, 9, 0, 0, 0, loc), "America/Chicago", true, src2)
	j3.Sweep(context.Background())
	if len(src2.cleared) != 0 {
		t.Fatalf("deleted a month whose email did not go: %v", src2.cleared)
	}
}
