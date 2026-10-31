package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/launchnotice"
)

type fakeNotices struct {
	clock   time.Time // CreatedAt of the rows it makes
	rows    map[uuid.UUID]*launchnotice.State
	claimed map[uuid.UUID]bool
}

func newFakeNotices() *fakeNotices {
	return &fakeNotices{clock: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), rows: map[uuid.UUID]*launchnotice.State{}, claimed: map[uuid.UUID]bool{}}
}

func (f *fakeNotices) Ensure(_ context.Context, uid uuid.UUID, _ string, p launchnotice.Params) (bool, error) {
	if _, ok := f.rows[uid]; ok {
		return false, nil
	}
	f.rows[uid] = &launchnotice.State{Exists: true, Params: p, CreatedAt: f.clock}
	return true, nil
}
func (f *fakeNotices) ClaimEmail(_ context.Context, uid uuid.UUID, _ string) (launchnotice.Params, bool, error) {
	r := f.rows[uid]
	if r == nil || r.EmailSent || f.claimed[uid] {
		return launchnotice.Params{}, false, nil
	}
	f.claimed[uid] = true
	return r.Params, true, nil
}
func (f *fakeNotices) MarkSent(_ context.Context, uid uuid.UUID, _ string) error {
	f.rows[uid].EmailSent = true
	return nil
}
func (f *fakeNotices) ReleaseClaim(_ context.Context, uid uuid.UUID, _ string) error {
	delete(f.claimed, uid)
	return nil
}
func (f *fakeNotices) states() map[uuid.UUID]launchnotice.State {
	out := map[uuid.UUID]launchnotice.State{}
	for id, r := range f.rows {
		out[id] = *r
	}
	return out
}

type recMailer struct {
	sent []email.Message
	fail map[string]bool
}

func (m *recMailer) Send(_ context.Context, msg email.Message) error {
	if m.fail[msg.To] {
		return errors.New("boom " + msg.To)
	}
	m.sent = append(m.sent, msg)
	return nil
}

var (
	uVerified   = uuid.MustParse("00000000-0000-4000-8000-000000000001")
	uVerified2  = uuid.MustParse("00000000-0000-4000-8000-000000000002")
	uUnverified = uuid.MustParse("00000000-0000-4000-8000-000000000003")
	uDisabled   = uuid.MustParse("00000000-0000-4000-8000-000000000004")
	uDemo       = uuid.MustParse("00000000-0000-4000-8000-000000000005")
)

func noticeDeps(f *fakeNotices, m *recMailer, out io.Writer, apply bool, now time.Time) launchNoticeDeps {
	return launchNoticeDeps{
		Rows: []announceUserRow{
			{ID: uVerified, Email: "a@example.test", Verified: true},
			{ID: uVerified2, Email: "b@example.test", Verified: true},
			{ID: uUnverified, Email: "c@example.test"},
			{ID: uDisabled, Email: "d@example.test", Verified: true, Disabled: true},
			{ID: uDemo, Email: "e@example.test", Verified: true},
		},
		States: f.states(), Store: f, Mailer: m, Out: out, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return now }, SkipIDs: map[uuid.UUID]bool{uDemo: true}, Excluded: map[uuid.UUID]bool{},
		Apply: apply, Launch: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), Interval: 0,
	}
}

var idOrAddr = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}|[0-9a-f]{8}\b|@example\.test`)

func TestLaunchNoticeDryRunPrintsCountsOnly(t *testing.T) {
	f, m := newFakeNotices(), &recMailer{}
	var out bytes.Buffer
	if code := launchNoticeRun(context.Background(), noticeDeps(f, m, &out, false, time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	s := out.String()
	for _, want := range []string{"in-app notice: to create 3", "email: to send 2", "unverified address 1", "disabled 1, demo/bypass 1", "new Terms take effect 2026-12-31"} {
		if !strings.Contains(s, want) {
			t.Errorf("dry run is missing %q:\n%s", want, s)
		}
	}
	if idOrAddr.MatchString(s) {
		t.Errorf("the dry run printed an id, prefix or address:\n%s", s)
	}
	if len(m.sent) != 0 || len(f.rows) != 0 {
		t.Errorf("a dry run wrote: %d sent, %d rows", len(m.sent), len(f.rows))
	}
}

func TestLaunchNoticeApplyIsIdempotentAndEngagementBlind(t *testing.T) {
	f, m := newFakeNotices(), &recMailer{}
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if code := launchNoticeRun(context.Background(), noticeDeps(f, m, &out, true, now)); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(m.sent) != 2 {
		t.Fatalf("%d emails, want 2 (the two verified accounts; unverified, disabled and demo get none)", len(m.sent))
	}
	if len(f.rows) != 3 || f.rows[uUnverified] == nil || f.rows[uDisabled] != nil || f.rows[uDemo] != nil {
		t.Errorf("in-app rows: %v, want verified x2 and unverified only", f.rows)
	}
	for _, msg := range m.sent {
		if strings.Contains(msg.Text, "LAUNCH_DATE") || !strings.Contains(msg.Text, "December 31, 2026") || msg.Subject != launchnotice.Subject {
			t.Errorf("bad email: %q %q", msg.Subject, msg.Text)
		}
		if !strings.HasPrefix(msg.IdempotencyKey, "launch-notice-") {
			t.Errorf("idempotency key %q", msg.IdempotencyKey)
		}
	}
	if !strings.Contains(out.String(), "emails sent 2, failed 0") || idOrAddr.MatchString(out.String()) {
		t.Errorf("final counts:\n%s", out.String())
	}
	// A second run sends nothing and creates nothing.
	out.Reset()
	if code := launchNoticeRun(context.Background(), noticeDeps(f, m, &out, true, now)); code != 0 || len(m.sent) != 2 || !strings.Contains(out.String(), "in-app notices created 0; emails sent 0") {
		t.Errorf("re-run: exit %d, %d sent total:\n%s", code, len(m.sent), out.String())
	}
}

func TestLaunchNoticeRetriesOnlyFailuresWithTheOriginalDates(t *testing.T) {
	f, m := newFakeNotices(), &recMailer{fail: map[string]bool{"b@example.test": true}}
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if code := launchNoticeRun(context.Background(), noticeDeps(f, m, &out, true, now)); code != 1 {
		t.Fatalf("exit %d, want 1 (one failed): %s", code, out.String())
	}
	if len(m.sent) != 1 || !strings.Contains(out.String(), "emails sent 1, failed 1") {
		t.Fatalf("first run: %d sent:\n%s", len(m.sent), out.String())
	}
	if f.claimed[uVerified2] {
		t.Error("a failed send kept its claim")
	}
	m.fail = nil
	out.Reset()
	if code := launchNoticeRun(context.Background(), noticeDeps(f, m, &out, true, now.Add(48*time.Hour))); code != 0 || len(m.sent) != 2 {
		t.Fatalf("retry: exit %d, %d sent:\n%s", code, len(m.sent), out.String())
	}
	if !strings.Contains(m.sent[1].Text, "December 31, 2026") {
		t.Errorf("the retried email states other dates:\n%s", m.sent[1].Text)
	}
}

func TestLaunchNoticeRefusals(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	// After the launch date: no apply (the Terms date would be under 30 days from the email). A dry run still reports.
	f, m := newFakeNotices(), &recMailer{}
	var out bytes.Buffer
	late := now.AddDate(0, 0, 30)
	if code := launchNoticeRun(context.Background(), noticeDeps(f, m, &out, true, late)); code != 1 || len(m.sent) != 0 || len(f.rows) != 0 || !strings.Contains(out.String(), "fewer than 30 days") {
		t.Errorf("after the launch date: exit %d, %d sent, %d rows:\n%s", code, len(m.sent), len(f.rows), out.String())
	}
	// Existing rows state other dates than --launch-date gives: refuse.
	f = newFakeNotices()
	f.rows[uVerified] = &launchnotice.State{Exists: true, Params: launchnotice.NewParams(time.Date(2026, 12, 8, 0, 0, 0, 0, time.UTC)), CreatedAt: now}
	out.Reset()
	if code := launchNoticeRun(context.Background(), noticeDeps(f, m, &out, true, now)); code != 1 || len(m.sent) != 0 || !strings.Contains(out.String(), "REFUSED") {
		t.Errorf("different dates: exit %d, %d sent:\n%s", code, len(m.sent), out.String())
	}
	// An unsent email on a row more than a week old: refuse.
	f = newFakeNotices()
	f.rows[uVerified] = &launchnotice.State{Exists: true, Params: launchnotice.NewParams(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)), CreatedAt: now.AddDate(0, 0, -8)}
	out.Reset()
	if code := launchNoticeRun(context.Background(), noticeDeps(f, m, &out, true, now)); code != 1 || len(m.sent) != 0 || !strings.Contains(out.String(), "REFUSED") {
		t.Errorf("stale retry: exit %d, %d sent:\n%s", code, len(m.sent), out.String())
	}
}

func TestLaunchNoticeTestSendWritesNothing(t *testing.T) {
	f, m := newFakeNotices(), &recMailer{}
	var out bytes.Buffer
	d := noticeDeps(f, m, &out, false, time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))
	d.Only = uVerified
	if code := launchNoticeRun(context.Background(), d); code != 0 || len(m.sent) != 1 || len(f.rows) != 0 {
		t.Errorf("exit %d, %d sent, %d rows:\n%s", code, len(m.sent), len(f.rows), out.String())
	}
	d.Only = uUnverified
	if code := launchNoticeRun(context.Background(), d); code != 1 || len(m.sent) != 1 {
		t.Errorf("an unverified test target: exit %d, %d sent", code, len(m.sent))
	}
}
