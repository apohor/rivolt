package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/idp"
)

type fakePurgeStore struct {
	cancelled int
	warnDue   []db.InactiveWarning
	claim     map[uuid.UUID]bool
	marked    []uuid.UUID
	deleteDue []uuid.UUID
	deleted   map[uuid.UUID]string // uid -> username; a missing uid is "no longer eligible"
	deleteErr map[uuid.UUID]error
	deleteLog []uuid.UUID
	counts    [3]int
	// accounts with no verified address to warn
	unwarnDue     []uuid.UUID
	unwarnDeleted map[uuid.UUID]string
	unwarnLog     []uuid.UUID
	unwarnCount   int
}

func (f *fakePurgeStore) CancelWarnings(context.Context) (int, error) { return f.cancelled, nil }
func (f *fakePurgeStore) WarnDue(context.Context, int) ([]db.InactiveWarning, error) {
	return f.warnDue, nil
}
func (f *fakePurgeStore) ClaimWarning(_ context.Context, uid uuid.UUID) (bool, error) {
	return f.claim[uid], nil
}
func (f *fakePurgeStore) MarkWarned(_ context.Context, uid uuid.UUID) error {
	f.marked = append(f.marked, uid)
	return nil
}
func (f *fakePurgeStore) DeleteDue(context.Context, int) ([]uuid.UUID, error) {
	return f.deleteDue, nil
}
func (f *fakePurgeStore) Delete(_ context.Context, uid uuid.UUID) (string, bool, error) {
	f.deleteLog = append(f.deleteLog, uid)
	if err := f.deleteErr[uid]; err != nil {
		return "", false, err
	}
	name, ok := f.deleted[uid]
	return name, ok, nil
}
func (f *fakePurgeStore) Counts(context.Context) (int, int, int, error) {
	return f.counts[0], f.counts[1], f.counts[2], nil
}
func (f *fakePurgeStore) UnwarnableDue(context.Context, int) ([]uuid.UUID, error) {
	return f.unwarnDue, nil
}
func (f *fakePurgeStore) DeleteUnwarnable(_ context.Context, uid uuid.UUID) (string, bool, error) {
	f.unwarnLog = append(f.unwarnLog, uid)
	name, ok := f.unwarnDeleted[uid]
	return name, ok, nil
}
func (f *fakePurgeStore) UnwarnableCount(context.Context) (int, error) { return f.unwarnCount, nil }

type fakeMailer struct {
	sent []email.Message
	err  error
}

func (m *fakeMailer) Send(_ context.Context, msg email.Message) error {
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

type fakeIdP struct{ deleted []string }

func (f *fakeIdP) Enabled() bool                                       { return true }
func (f *fakeIdP) CreateUser(context.Context, idp.CreateRequest) error { return nil }
func (f *fakeIdP) CreateUserGeneratePassword(context.Context, idp.CreateRequest) (string, error) {
	return "", nil
}
func (f *fakeIdP) DeleteUser(_ context.Context, username string) error {
	f.deleted = append(f.deleted, username)
	return nil
}

func newTestPurge(st *fakePurgeStore, state flags.InactivePurgeState, m *fakeMailer, buf *bytes.Buffer) (*InactivePurge, *fakeIdP) {
	fi := &fakeIdP{}
	j := newInactivePurge(st, nil, func() flags.InactivePurgeState { return state }, fi, "https://rivolt.dev", slog.New(slog.NewTextHandler(buf, nil)))
	if m != nil {
		j.mailer = m
	}
	return j, fi
}

func TestInactivePurgeOffDoesNothing(t *testing.T) {
	st := &fakePurgeStore{}
	var buf bytes.Buffer
	j, _ := newTestPurge(st, flags.InactivePurgeState{}, &fakeMailer{}, &buf)
	if res := j.RunOnce(context.Background()); !res.Off || buf.Len() != 0 {
		t.Errorf("flag off: %+v log %q", res, buf.String())
	}
}

// A dry run logs the counts and nothing else: no ids, no emails, no sends,
// no deletes, even with All set.
func TestInactivePurgeDryRunLogsCountsOnly(t *testing.T) {
	uid := uuid.New()
	st := &fakePurgeStore{counts: [3]int{4, 2, 7}, warnDue: []db.InactiveWarning{{UserID: uid, Email: "someone@example.com"}}, deleteDue: []uuid.UUID{uid}}
	m := &fakeMailer{}
	var buf bytes.Buffer
	j, fi := newTestPurge(st, flags.InactivePurgeState{DryRun: true, All: true}, m, &buf)
	res := j.RunOnce(context.Background())
	if !res.DryRun || res.WarnDue != 4 || res.DeleteDue != 2 || res.WarnedWaiting != 7 {
		t.Errorf("result = %+v", res)
	}
	if len(m.sent) != 0 || len(st.deleteLog) != 0 || len(st.marked) != 0 || len(fi.deleted) != 0 {
		t.Errorf("a dry run acted: sent %d deleted %v marked %v idp %v", len(m.sent), st.deleteLog, st.marked, fi.deleted)
	}
	out := buf.String()
	if !strings.Contains(out, "warn_due=4") || !strings.Contains(out, "delete_due=2") {
		t.Errorf("counts not logged: %s", out)
	}
	if strings.Contains(out, uid.String()) || strings.Contains(out, "example.com") || strings.Contains(out, "user_id") {
		t.Errorf("a dry run logged an id or an email: %s", out)
	}
}

func TestInactivePurgeWarnsOnceWithTheDateAndRecordsIt(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	deleteOn := time.Date(2027, 2, 14, 0, 0, 0, 0, time.UTC)
	st := &fakePurgeStore{
		warnDue: []db.InactiveWarning{
			{UserID: a, Email: "a@example.com", DeleteOn: deleteOn},
			{UserID: b, Email: "b@example.com", DeleteOn: deleteOn}, // claim lost (another replica / just signed in)
			{UserID: c, Email: "c@example.com", DeleteOn: deleteOn}, // not in the rehearsal list
		},
		claim: map[uuid.UUID]bool{a: true, b: false, c: true},
	}
	m := &fakeMailer{}
	var buf bytes.Buffer
	j, _ := newTestPurge(st, flags.InactivePurgeState{Users: []string{a.String(), b.String()}}, m, &buf)
	res := j.RunOnce(context.Background())
	if res.Warned != 1 || len(m.sent) != 1 || len(st.marked) != 1 || st.marked[0] != a {
		t.Fatalf("warned %d sent %d marked %v", res.Warned, len(m.sent), st.marked)
	}
	msg := m.sent[0]
	if msg.To != "a@example.com" || !strings.Contains(msg.Subject, "February 14, 2027") || !strings.Contains(msg.Text, "February 14, 2027") ||
		!strings.Contains(msg.Text, "unless you connect your Rivian") || !strings.Contains(msg.Text, "https://rivolt.dev/") ||
		msg.IdempotencyKey != "inactive-warning-"+a.String() {
		t.Errorf("message = %+v", msg)
	}
	if strings.Contains(buf.String(), "example.com") {
		t.Errorf("an email address was logged: %s", buf.String())
	}
}

// A failed send is not a warning: it is not recorded, so it is retried and an
// account that was never told is never deleted.
func TestInactivePurgeFailedSendIsNotRecorded(t *testing.T) {
	a := uuid.New()
	st := &fakePurgeStore{warnDue: []db.InactiveWarning{{UserID: a, Email: "a@example.com", DeleteOn: time.Now()}}, claim: map[uuid.UUID]bool{a: true}}
	m := &fakeMailer{err: errors.New(`resend: 422 invalid recipient a@example.com`)}
	var buf bytes.Buffer
	j, _ := newTestPurge(st, flags.InactivePurgeState{All: true}, m, &buf)
	res := j.RunOnce(context.Background())
	if res.Warned != 0 || len(st.marked) != 0 {
		t.Errorf("warned %d marked %v after a failed send", res.Warned, st.marked)
	}
	if strings.Contains(buf.String(), "a@example.com") {
		t.Errorf("the recipient leaked into the log: %s", buf.String())
	}
}

func TestInactivePurgeDeletesThroughTheIdPAndSkipsIneligible(t *testing.T) {
	gone, kept, rehearsal := uuid.New(), uuid.New(), uuid.New()
	st := &fakePurgeStore{
		deleteDue: []uuid.UUID{gone, kept, rehearsal},
		deleted:   map[uuid.UUID]string{gone: "gone@example.com", rehearsal: "other@example.com"}, // kept: no longer eligible
	}
	var buf bytes.Buffer
	j, fi := newTestPurge(st, flags.InactivePurgeState{Users: []string{gone.String(), kept.String()}}, &fakeMailer{}, &buf)
	res := j.RunOnce(context.Background())
	if res.Deleted != 1 || len(fi.deleted) != 1 || fi.deleted[0] != "gone@example.com" {
		t.Errorf("deleted %d, idp %v", res.Deleted, fi.deleted)
	}
	for _, id := range st.deleteLog {
		if id == rehearsal {
			t.Error("deleted an account outside the rehearsal list")
		}
	}
}

func TestInactivePurgeBoundsADailyRun(t *testing.T) {
	st := &fakePurgeStore{deleted: map[uuid.UUID]string{}}
	for i := 0; i < purgeMaxDeletePerRun+10; i++ {
		id := uuid.New()
		st.deleteDue = append(st.deleteDue, id)
		st.deleted[id] = "u@example.com"
	}
	var buf bytes.Buffer
	j, _ := newTestPurge(st, flags.InactivePurgeState{All: true}, &fakeMailer{}, &buf)
	if res := j.RunOnce(context.Background()); res.Deleted != purgeMaxDeletePerRun {
		t.Errorf("deleted %d in one run, want the cap %d", res.Deleted, purgeMaxDeletePerRun)
	}
}

// Deleting requires a mailer: without one nothing can have been warned.
func TestInactivePurgeNeedsAMailerToAct(t *testing.T) {
	a := uuid.New()
	st := &fakePurgeStore{deleteDue: []uuid.UUID{a}, deleted: map[uuid.UUID]string{a: "x"}}
	var buf bytes.Buffer
	j, _ := newTestPurge(st, flags.InactivePurgeState{All: true}, nil, &buf)
	if res := j.RunOnce(context.Background()); res.Deleted != 0 || len(st.deleteLog) != 0 {
		t.Errorf("acted without a mailer: %+v", res)
	}
}

func TestInactivePurgeStateEnabled(t *testing.T) {
	u := uuid.New().String()
	for name, tc := range map[string]struct {
		s    flags.InactivePurgeState
		want bool
	}{
		"empty":             {flags.InactivePurgeState{}, false},
		"all":               {flags.InactivePurgeState{All: true}, true},
		"listed":            {flags.InactivePurgeState{Users: []string{strings.ToUpper(u)}}, true},
		"other":             {flags.InactivePurgeState{Users: []string{uuid.New().String()}}, false},
		"dry run beats all": {flags.InactivePurgeState{DryRun: true, All: true}, false},
	} {
		if got := tc.s.Enabled(u); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// An account with no verified address cannot be warned, so it is deleted at day
// 120 with no mail, even without a mailer; the IdP removal runs; an account that
// verified or connected since the list is skipped; a rehearsal list limits it like
// the warned deletions.
func TestInactivePurgeDeletesUnwarnableAccountsWithoutAMail(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	st := &fakePurgeStore{
		unwarnDue:     []uuid.UUID{a, b, c},
		unwarnDeleted: map[uuid.UUID]string{a: "a-user", c: "c-user"}, // b connected since the list was built
	}
	var buf bytes.Buffer
	j, fi := newTestPurge(st, flags.InactivePurgeState{Users: []string{a.String(), b.String()}}, nil, &buf) // no mailer; c not in the list
	res := j.RunOnce(context.Background())
	if res.UnwarnableDeleted != 1 || len(fi.deleted) != 1 || fi.deleted[0] != "a-user" {
		t.Errorf("result %+v, idp %v, want only a deleted", res, fi.deleted)
	}
	for _, u := range st.unwarnLog {
		if u == c {
			t.Error("an account outside the rehearsal list was touched")
		}
	}
}

func TestInactivePurgeDryRunCountsUnwarnableAccounts(t *testing.T) {
	st := &fakePurgeStore{unwarnCount: 3, unwarnDue: []uuid.UUID{uuid.New()}}
	var buf bytes.Buffer
	j, _ := newTestPurge(st, flags.InactivePurgeState{DryRun: true, All: true}, &fakeMailer{}, &buf)
	res := j.RunOnce(context.Background())
	if res.UnwarnableDue != 3 || len(st.unwarnLog) != 0 || !strings.Contains(buf.String(), "unwarnable_due=3") {
		t.Errorf("dry run: %+v, deleted %v, log %s", res, st.unwarnLog, buf.String())
	}
}

func TestInactivePurgeOffNeverTouchesUnwarnableAccounts(t *testing.T) {
	st := &fakePurgeStore{unwarnDue: []uuid.UUID{uuid.New()}, unwarnDeleted: map[uuid.UUID]string{}}
	var buf bytes.Buffer
	j, _ := newTestPurge(st, flags.InactivePurgeState{}, &fakeMailer{}, &buf)
	if res := j.RunOnce(context.Background()); !res.Off || len(st.unwarnLog) != 0 {
		t.Errorf("flag off: %+v, touched %v", res, st.unwarnLog)
	}
}
