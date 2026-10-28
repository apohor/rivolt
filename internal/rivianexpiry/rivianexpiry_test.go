package rivianexpiry

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
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/statement"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

var testSigner = statement.NewSigner([]byte("0123456789abcdef0123456789abcdef"))

type fakeSessions []Entry

func (f fakeSessions) List(context.Context) ([]Entry, error) { return f, nil }

// fakeClaims is the claim table: (user, kind, key) once.
type fakeClaims struct {
	engage.Store
	mu     sync.Mutex
	rows   map[string]string
	budget []bool
}

func (f *fakeClaims) Claim(_ context.Context, m engage.Msg, _, _ time.Time, _ int) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[string]string{}
	}
	f.budget = append(f.budget, !m.Utility)
	k := m.User.String() + "|" + m.Kind + "|" + m.Key
	if _, ok := f.rows[k]; ok {
		return false, false, nil
	}
	f.rows[k] = "sending"
	return true, false, nil
}

func (f *fakeClaims) Mark(_ context.Context, uid uuid.UUID, kind, key, status string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[uid.String()+"|"+kind+"|"+key] = status
	return nil
}

type fakeMail struct {
	sent []email.Message
	err  error
}

func (m *fakeMail) Send(_ context.Context, msg email.Message) error {
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

// signedIn is the sign-in time such that exactly `left` remains of the 180 days at t0.
func signedIn(left time.Duration) time.Time {
	return t0.Add(left - rivian.SessionLifetime)
}

func entry(left time.Duration) Entry {
	return Entry{User: uuid.New(), SignedIn: signedIn(left)}
}

func job(sessions []Entry, mail *fakeMail, claims *fakeClaims) (*Job, *Counts, map[string]int) {
	var counts Counts
	notices := map[string]int{}
	j := &Job{
		Sessions: fakeSessions(sessions),
		Claims:   claims,
		Mail:     mail,
		Email:    func(context.Context, uuid.UUID) (string, error) { return "driver@example.invalid", nil },
		BaseURL:  "https://rivolt.example",
		Signer:   func() *statement.Signer { return testSigner },
		Now:      func() time.Time { return t0 },
		OnCounts: func(c Counts) { counts = c },
		OnNotice: func(days int, outcome string) { notices[outcome+":"+string(rune('0'+days))]++ },
	}
	return j, &counts, notices
}

const day = 24 * time.Hour

func TestCountsAndNoticesByTimeLeft(t *testing.T) {
	ent := []Entry{
		entry(100 * day),      // not near
		entry(13*day + 3600),  // 14d window only
		entry(6 * day),        // 7d notice
		entry(3 * day),        // 7d notice
		entry(20 * time.Hour), // 1d notice
		entry(-time.Hour),     // already past
	}
	mail, claims := &fakeMail{}, &fakeClaims{}
	j, counts, notices := job(ent, mail, claims)
	j.Sweep(context.Background())

	if *counts != (Counts{Within14: 4, Within7: 3, Within1: 1, Expired: 1}) {
		t.Fatalf("counts = %+v, want 14d:4 7d:3 1d:1 expired:1", *counts)
	}
	if len(mail.sent) != 3 {
		t.Fatalf("%d emails, want 3 (two at 7 days, one at 1 day)", len(mail.sent))
	}
	if notices["sent:7"] != 2 || notices["sent:1"] != 1 {
		t.Fatalf("notices = %v", notices)
	}
	for _, b := range claims.budget {
		if b {
			t.Fatal("an expiry notice must not count against the 4-a-month engagement budget")
		}
	}
}

// The owner's N49 wording, with the date the connection ends, and a one-click way to stop.
func TestNoticeUsesTheApprovedWordingAndCarriesAStopLink(t *testing.T) {
	e := entry(3 * day) // ends Oct 31, 2026 at 12:00 UTC
	e.User = uuid.MustParse("11111111-2222-3333-4444-555555555555")
	mail, claims := &fakeMail{}, &fakeClaims{}
	j, _, _ := job([]Entry{e}, mail, claims)
	j.Sweep(context.Background())
	if len(mail.sent) != 1 {
		t.Fatalf("%d emails", len(mail.sent))
	}
	m := mail.sent[0]
	if m.Subject != "Reconnect Rivian to keep recording" {
		t.Fatalf("subject = %q", m.Subject)
	}
	for _, want := range []string{
		"Your Rivian connection in Rivolt expires on October 31, 2026.",
		"Rivian asks for a fresh sign-in every 180 days. After that date Rivolt stops recording drives and charges until you reconnect.",
		"Reconnect now: Settings → Rivian account → Sign in. It takes a minute and keeps your history.",
		"https://rivolt.example/settings#rivian",
		"Stop these reminders: https://rivolt.example/api/rivian-expiry-email/unsubscribe?t=",
	} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("the email lacks %q:\n%s", want, m.Text)
		}
	}
	// The link is signed for this user and only for these reminders.
	i := strings.Index(m.Text, "unsubscribe?t=")
	tok := strings.TrimSpace(m.Text[i+len("unsubscribe?t="):])
	uid, purpose, err := testSigner.Verify(tok, t0)
	if err != nil || uid != e.User || purpose != UnsubPurpose {
		t.Fatalf("token verifies as %v %q %v, want the user and %q", uid, purpose, err, UnsubPurpose)
	}
	if m.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || !strings.Contains(m.Headers["List-Unsubscribe"], "/api/rivian-expiry-email/unsubscribe?t=") {
		t.Fatalf("headers = %v, want a one-click List-Unsubscribe", m.Headers)
	}
}

func TestAUserWhoStoppedTheRemindersGetsNoneAndNothingIsClaimed(t *testing.T) {
	e := entry(3 * day)
	e.OptedOut = true
	mail, claims := &fakeMail{}, &fakeClaims{}
	j, counts, notices := job([]Entry{e}, mail, claims)
	j.Sweep(context.Background())
	if len(mail.sent) != 0 || len(claims.rows) != 0 || notices["opted_out:7"] != 1 {
		t.Fatalf("sent=%d claims=%v notices=%v", len(mail.sent), claims.rows, notices)
	}
	if counts.Within7 != 1 {
		t.Fatalf("an opted-out session still counts for the metric: %+v", *counts)
	}
}

func TestNothingIsSentWithoutASignerBecauseThereWouldBeNoWayToStop(t *testing.T) {
	e := entry(3 * day)
	mail, claims := &fakeMail{}, &fakeClaims{}
	j, _, notices := job([]Entry{e}, mail, claims)
	j.Signer = func() *statement.Signer { return nil }
	j.Sweep(context.Background())
	if len(mail.sent) != 0 || len(claims.rows) != 0 || notices["no_signer:7"] != 1 {
		t.Fatalf("sent=%d claims=%v notices=%v", len(mail.sent), claims.rows, notices)
	}
	j.Signer = nil
	j.Sweep(context.Background())
	if len(mail.sent) != 0 {
		t.Fatal("sent with no Signer func")
	}
}

func TestEachNoticeGoesOutOnceAcrossSweepsAndReplicas(t *testing.T) {
	e := entry(5 * day)
	mail, claims := &fakeMail{}, &fakeClaims{}
	a, _, _ := job([]Entry{e}, mail, claims)
	b, _, _ := job([]Entry{e}, mail, claims) // another replica, same shared claim table
	a.Sweep(context.Background())
	b.Sweep(context.Background())
	a.Sweep(context.Background())
	if len(mail.sent) != 1 {
		t.Fatalf("%d emails for one session across replicas and sweeps, want 1", len(mail.sent))
	}
	// Later, with under a day left, the final notice is a different key.
	later := e
	later.SignedIn = later.SignedIn.Add(-4*day - 12*time.Hour) // 12 h left
	c, _, _ := job([]Entry{later}, mail, claims)
	c.Sweep(context.Background())
	if len(mail.sent) != 2 {
		t.Fatalf("%d emails after the final window opened, want 2", len(mail.sent))
	}
}

func TestANewSignInStartsOver(t *testing.T) {
	e := entry(5 * day)
	mail, claims := &fakeMail{}, &fakeClaims{}
	j, _, _ := job([]Entry{e}, mail, claims)
	j.Sweep(context.Background())
	// The user signs in again: 180 days left, nothing sent, and when that session nears its
	// end a new notice is due.
	e.SignedIn = t0
	j2, counts, _ := job([]Entry{e}, mail, claims)
	j2.Sweep(context.Background())
	if len(mail.sent) != 1 || counts.Within14 != 0 {
		t.Fatalf("a fresh session must not be warned about: sent=%d counts=%+v", len(mail.sent), *counts)
	}
	e.SignedIn = t0.Add(-176 * day) // the new session, 4 days from its end
	j3, _, _ := job([]Entry{e}, mail, claims)
	j3.Sweep(context.Background())
	if len(mail.sent) != 2 {
		t.Fatalf("the new session's own warning was not sent: %d", len(mail.sent))
	}
}

func TestSkipsFlaggedDemoAndSessionsWithoutASignInTime(t *testing.T) {
	flagged := entry(3 * day)
	flagged.NeedsReauth = true
	demoUser := entry(3 * day)
	noTime := entry(3 * day)
	noTime.SignedIn = time.Time{}
	mail, claims := &fakeMail{}, &fakeClaims{}
	j, counts, _ := job([]Entry{flagged, demoUser, noTime}, mail, claims)
	j.Skip = func(uid uuid.UUID) bool { return uid == demoUser.User }
	j.Sweep(context.Background())
	if len(mail.sent) != 0 || *counts != (Counts{}) {
		t.Fatalf("sent=%d counts=%+v, want nothing for a flagged, demo or untimed session", len(mail.sent), *counts)
	}
}

func TestNoAddressClaimsNothingSoALaterAddressStillGetsIt(t *testing.T) {
	e := entry(3 * day)
	mail, claims := &fakeMail{}, &fakeClaims{}
	j, _, notices := job([]Entry{e}, mail, claims)
	addr := ""
	j.Email = func(context.Context, uuid.UUID) (string, error) { return addr, nil }
	j.Sweep(context.Background())
	if len(claims.rows) != 0 || notices["no_email:7"] != 1 {
		t.Fatalf("claimed %v, notices %v", claims.rows, notices)
	}
	addr = "driver@example.invalid"
	j.Sweep(context.Background())
	if len(mail.sent) != 1 {
		t.Fatal("the notice was not sent once an address existed")
	}
}

func TestFailedSendIsRecordedAndNotRetriedUnderTheSameKey(t *testing.T) {
	e := entry(3 * day)
	mail, claims := &fakeMail{err: errors.New("resend down")}, &fakeClaims{}
	j, _, notices := job([]Entry{e}, mail, claims)
	j.Sweep(context.Background())
	j.Sweep(context.Background())
	if notices["failed:7"] != 1 || notices["already_sent:7"] != 1 {
		t.Fatalf("notices = %v, want one failure then the claim holding", notices)
	}
}

func TestCountingWorksWithoutAMailer(t *testing.T) {
	e := entry(3 * day)
	claims := &fakeClaims{}
	j, counts, notices := job([]Entry{e}, nil, claims)
	j.Mail = nil
	j.Sweep(context.Background())
	if counts.Within7 != 1 || notices["no_mailer:7"] != 1 || len(claims.rows) != 0 {
		t.Fatalf("counts=%+v notices=%v claims=%v", *counts, notices, claims.rows)
	}
}

func TestLifetimeIsConfigurable(t *testing.T) {
	e := entry(3 * day) // 3 days left of 180
	mail, claims := &fakeMail{}, &fakeClaims{}
	j, counts, _ := job([]Entry{e}, mail, claims)
	j.Lifetime = 365 * day // with a year, 3 days into 365 is nowhere near the end
	j.Sweep(context.Background())
	if len(mail.sent) != 0 || counts.Within14 != 0 {
		t.Fatalf("sent=%d counts=%+v with a 365-day lifetime", len(mail.sent), *counts)
	}
}
