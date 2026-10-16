package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
)

type fakeAnnounceMailer struct {
	sent   []email.Message
	failTo map[string]bool
}

func (f *fakeAnnounceMailer) Send(_ context.Context, m email.Message) error {
	if f.failTo[m.To] {
		return errors.New("resend 422: invalid address " + m.To)
	}
	f.sent = append(f.sent, m)
	return nil
}

func annRows() ([]announceUserRow, map[string]uuid.UUID) {
	ids := map[string]uuid.UUID{}
	mk := func(name, addr string, disabled, verified bool) announceUserRow {
		ids[name] = uuid.New()
		return announceUserRow{ID: ids[name], Email: addr, Disabled: disabled, Verified: verified}
	}
	rows := []announceUserRow{
		mk("a", "a@example.com", false, true),
		mk("b", "b@example.com", false, true),
		mk("disabled", "d@example.com", true, true),
		mk("unverified", "u@example.com", false, false),
		mk("noaddr", "", false, true),
		mk("junk", "not an address", false, true),
		mk("display", "Bob <bob@example.com>", false, true),
		mk("bypass", "dev@example.com", false, true),
		mk("qa", "qa@example.com", false, true),
	}
	return rows, ids
}

func annDeps(rows []announceUserRow, ids map[string]uuid.UUID, m announceMailer, out *bytes.Buffer, logBuf *bytes.Buffer) announceDeps {
	return announceDeps{
		List:     func(context.Context) ([]announceUserRow, error) { return rows, nil },
		Mailer:   m,
		Out:      out,
		Logger:   slog.New(slog.NewJSONHandler(logBuf, nil)),
		Sleep:    func(time.Duration) {},
		SkipIDs:  map[uuid.UUID]bool{ids["bypass"]: true},
		Excluded: map[uuid.UUID]bool{ids["qa"]: true},
		Subject:  "Rivolt has a new look",
		Body:     "Hi,\n\nnew layout.\n",
	}
}

func TestAnnounceDryRunPrintsPrefixesOnly(t *testing.T) {
	rows, ids := annRows()
	var out, logs bytes.Buffer
	m := &fakeAnnounceMailer{}
	if code := announceRun(context.Background(), annDeps(rows, ids, m, &out, &logs)); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if len(m.sent) != 0 {
		t.Fatalf("dry run sent %d", len(m.sent))
	}
	s := out.String()
	if !strings.Contains(s, "2 recipient(s)") ||
		!strings.Contains(s, "disabled 1, unverified 1, bad address 3, bypass/demo 1, excluded 1") {
		t.Errorf("report:\n%s", s)
	}
	if strings.Contains(s, "@") || strings.Contains(s, ids["a"].String()) {
		t.Errorf("dry run leaked an address or full id:\n%s", s)
	}
	if !strings.Contains(s, ids["a"].String()[:8]) {
		t.Errorf("missing id prefix:\n%s", s)
	}
}

func TestAnnounceApplySendsOneEachAndResumes(t *testing.T) {
	rows, ids := annRows()
	logPath := filepath.Join(t.TempDir(), "sent.log")
	var out, logs bytes.Buffer
	m := &fakeAnnounceMailer{}
	d := annDeps(rows, ids, m, &out, &logs)
	d.Apply, d.SentLog = true, logPath
	if code := announceRun(context.Background(), d); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if len(m.sent) != 2 {
		t.Fatalf("sent %d, want 2", len(m.sent))
	}
	for _, msg := range m.sent {
		if strings.Contains(msg.To, ",") || msg.Subject != d.Subject || msg.Text != d.Body || msg.IdempotencyKey == "" {
			t.Errorf("message = %+v", msg)
		}
	}
	if strings.Contains(logs.String(), "@") {
		t.Errorf("logs leaked an address: %s", logs.String())
	}
	// A re-run skips everyone already in the sent log.
	m2 := &fakeAnnounceMailer{}
	out.Reset()
	d2 := annDeps(rows, ids, m2, &out, &logs)
	d2.Apply, d2.SentLog = true, logPath
	if code := announceRun(context.Background(), d2); code != 0 || len(m2.sent) != 0 || !strings.Contains(out.String(), "already sent 2") {
		t.Errorf("rerun: code %d sent %d\n%s", code, len(m2.sent), out.String())
	}
}

func TestAnnounceResumesAfterPartialAndScrubsErrors(t *testing.T) {
	rows, ids := annRows()
	logPath := filepath.Join(t.TempDir(), "sent.log")
	var out, logs bytes.Buffer
	m := &fakeAnnounceMailer{failTo: map[string]bool{"b@example.com": true}}
	d := annDeps(rows, ids, m, &out, &logs)
	d.Apply, d.SentLog = true, logPath
	if code := announceRun(context.Background(), d); code != 1 {
		t.Fatalf("code %d, want 1 with a failure", code)
	}
	if strings.Contains(logs.String(), "@") || strings.Contains(logs.String(), "example.com") {
		t.Errorf("error log not scrubbed: %s", logs.String())
	}
	logged, _ := os.ReadFile(logPath)
	if strings.TrimSpace(string(logged)) != ids["a"].String() {
		t.Errorf("sent log = %q, want only the successful user", logged)
	}
	// Retry: only the failed one goes out.
	m2 := &fakeAnnounceMailer{}
	d2 := annDeps(rows, ids, m2, &out, &logs)
	d2.Apply, d2.SentLog = true, logPath
	if code := announceRun(context.Background(), d2); code != 0 || len(m2.sent) != 1 || m2.sent[0].To != "b@example.com" {
		t.Errorf("retry: code %d sent %+v", code, m2.sent)
	}
}

func TestAnnounceTestToSendsOneAndLeavesLogAlone(t *testing.T) {
	rows, ids := annRows()
	logPath := filepath.Join(t.TempDir(), "sent.log")
	var out, logs bytes.Buffer
	m := &fakeAnnounceMailer{}
	d := annDeps(rows, ids, m, &out, &logs)
	d.Only, d.SentLog = ids["a"], logPath
	if code := announceRun(context.Background(), d); code != 0 || len(m.sent) != 1 || m.sent[0].To != "a@example.com" {
		t.Fatalf("test send: code %d sent %+v\n%s", code, m.sent, out.String())
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("test send wrote the sent log")
	}
	// Ineligible test target is refused, nothing sent.
	m3 := &fakeAnnounceMailer{}
	d3 := annDeps(rows, ids, m3, &out, &logs)
	d3.Only = ids["disabled"]
	if code := announceRun(context.Background(), d3); code == 0 || len(m3.sent) != 0 {
		t.Errorf("disabled test target: code %d sent %d", code, len(m3.sent))
	}
}

func TestAnnounceBodyPlaceholderRefused(t *testing.T) {
	for _, b := range []string{"", "  \n", "write to [DECIDE: contact address]", "TODO footer"} {
		if checkAnnounceBody(b) == nil {
			t.Errorf("accepted %q", b)
		}
	}
	if err := checkAnnounceBody("Hi,\n\nall good.\n"); err != nil {
		t.Error(err)
	}
}
