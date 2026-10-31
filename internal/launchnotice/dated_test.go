package launchnotice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
)

type fakeDated struct {
	cands    []FoundingCandidate
	prep     map[uuid.UUID]Prepared
	sent     map[uuid.UUID]bool
	released []uuid.UUID
}

func (f *fakeDated) FoundingCandidates(context.Context, time.Time) ([]FoundingCandidate, error) {
	var out []FoundingCandidate
	for _, c := range f.cands {
		if !f.sent[c.UserID] {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeDated) PrepareFounding(_ context.Context, c FoundingCandidate, _ time.Time, wantEmail bool) (Prepared, error) {
	p := f.prep[c.UserID]
	p.Claimed = p.Claimed && wantEmail
	return p, nil
}
func (f *fakeDated) MarkSent(_ context.Context, uid uuid.UUID, _ string) error {
	f.sent[uid] = true
	return nil
}
func (f *fakeDated) ReleaseClaim(_ context.Context, uid uuid.UUID, _ string) error {
	f.released = append(f.released, uid)
	return nil
}

type recMail struct {
	msgs []email.Message
	fail bool
}

func (m *recMail) Send(_ context.Context, msg email.Message) error {
	if m.fail {
		return errors.New("boom")
	}
	m.msgs = append(m.msgs, msg)
	return nil
}

var (
	idA = uuid.MustParse("00000000-0000-4000-8000-0000000000a1")
	idB = uuid.MustParse("00000000-0000-4000-8000-0000000000b2")
	idC = uuid.MustParse("00000000-0000-4000-8000-0000000000c3")
	idD = uuid.MustParse("00000000-0000-4000-8000-0000000000d4")
)

func datedJob(f *fakeDated, m *recMail) (*DatedJob, *[]string) {
	var seen []string
	return &DatedJob{Store: f, Mailer: m, Now: func() time.Time { return day("2027-10-01") }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Observe: func(kind, outcome string) { seen = append(seen, kind+"/"+outcome) }}, &seen
}

func TestFoundingNoticeSendsOncePerAccount(t *testing.T) {
	f := &fakeDated{
		cands: []FoundingCandidate{
			{UserID: idA, Email: "a@example.test", Verified: true, End: day("2027-11-15")},
			{UserID: idB, Email: "b@example.test", Verified: true, End: day("2027-11-15"), Subscribed: true},
			{UserID: idC, Email: "c@example.test", Verified: false, End: day("2027-11-15")},
		},
		prep: map[uuid.UUID]Prepared{
			idA: {Params: FoundingParams{End: "2027-11-15"}, Claimed: true},
			idB: {Params: FoundingParams{End: "2027-11-15", Subscribed: true}, Claimed: true},
			idC: {Params: FoundingParams{End: "2027-11-15"}},
		},
		sent: map[uuid.UUID]bool{},
	}
	m := &recMail{}
	j, seen := datedJob(f, m)
	st, err := j.RunOnce(context.Background())
	if err != nil || st.Sent != 2 || st.InApp != 3 || st.Late != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	if len(m.msgs) != 2 || m.msgs[0].Subject != "Your free year of Rivolt Pro ends on November 15, 2027" {
		t.Fatalf("emails: %+v", m.msgs)
	}
	if strings.Contains(m.msgs[0].Text, "subscription continues") || !strings.Contains(m.msgs[1].Text, "Your Rivolt Pro subscription continues; nothing changes for you on November 15, 2027.") {
		t.Errorf("the subscriber variant is wrong:\n%s\n---\n%s", m.msgs[0].Text, m.msgs[1].Text)
	}
	if !strings.HasPrefix(m.msgs[0].IdempotencyKey, "founding-end-") || strings.Contains(m.msgs[0].Text, "FOUNDING_END_DATE") {
		t.Errorf("key %q / placeholder", m.msgs[0].IdempotencyKey)
	}
	// Unverified: in-app only. A second run finds nothing to send.
	for _, msg := range m.msgs {
		if msg.To == "c@example.test" {
			t.Error("an unverified address got an email")
		}
	}
	if st, _ := j.RunOnce(context.Background()); st.Sent != 0 || len(m.msgs) != 2 {
		t.Errorf("second run sent more: %+v, %d emails", st, len(m.msgs))
	}
	if len(*seen) == 0 {
		t.Error("no outcomes counted")
	}
}

func TestLateFoundingNoticeIsExtendedAndCounted(t *testing.T) {
	f := &fakeDated{
		cands: []FoundingCandidate{{UserID: idA, Email: "a@example.test", Verified: true, End: day("2027-10-20")}},
		prep: map[uuid.UUID]Prepared{idA: {Params: FoundingParams{End: "2027-10-31", ExtendedFrom: "2027-10-20"}, Claimed: true, Extended: true,
			Shortfall: 11 * 24 * time.Hour}},
		sent: map[uuid.UUID]bool{},
	}
	m := &recMail{}
	var extended []uuid.UUID
	j, seen := datedJob(f, m)
	j.OnExtend = func(uid uuid.UUID) { extended = append(extended, uid) }
	st, err := j.RunOnce(context.Background())
	if err != nil || st.Late != 1 || st.WorstShortfall != 11*24*time.Hour || st.Sent != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	if len(extended) != 1 || extended[0] != idA {
		t.Errorf("the plan cache was not invalidated: %v", extended)
	}
	body := m.msgs[0].Text
	if !strings.Contains(body, "ends on October 31, 2027") || !strings.Contains(body, "We moved this date from October 20, 2027 to October 31, 2027 so that you have at least 30 days' notice.") {
		t.Errorf("the email does not state the extended date:\n%s", body)
	}
	found := false
	for _, s := range *seen {
		if s == "founding_end/late" {
			found = true
		}
	}
	if !found {
		t.Errorf("the late case was not counted: %v", *seen)
	}
}

func TestFoundingNoticeFailureReleasesTheClaim(t *testing.T) {
	f := &fakeDated{
		cands: []FoundingCandidate{{UserID: idA, Email: "a@example.test", Verified: true, End: day("2027-11-15")}},
		prep:  map[uuid.UUID]Prepared{idA: {Params: FoundingParams{End: "2027-11-15"}, Claimed: true}},
		sent:  map[uuid.UUID]bool{},
	}
	j, _ := datedJob(f, &recMail{fail: true})
	st, err := j.RunOnce(context.Background())
	if err != nil || st.Failed != 1 || st.Sent != 0 || len(f.released) != 1 || f.sent[idA] {
		t.Errorf("%+v %v released=%v sent=%v", st, err, f.released, f.sent)
	}
}

func TestFoundingNoticeSkipsExcludedAccounts(t *testing.T) {
	f := &fakeDated{cands: []FoundingCandidate{{UserID: idD, Email: "d@example.test", Verified: true, End: day("2027-11-15")}},
		prep: map[uuid.UUID]Prepared{idD: {Params: FoundingParams{End: "2027-11-15"}, Claimed: true}}, sent: map[uuid.UUID]bool{}}
	m := &recMail{}
	j, _ := datedJob(f, m)
	j.Skip = map[uuid.UUID]bool{idD: true}
	if st, _ := j.RunOnce(context.Background()); st.Considered != 0 || len(m.msgs) != 0 {
		t.Errorf("an excluded account was noticed: %+v", st)
	}
}

func TestFoundingCopy(t *testing.T) {
	p := FoundingParams{End: "2027-11-15"}
	if tok := Unfilled(RenderFoundingEmail(p) + FoundingSubject(p)); tok != "" {
		t.Errorf("leftover %q", tok)
	}
	for _, want := range []string{"November 15, 2027", "the full export", "not affiliated with Rivian", "Nothing is charged and nothing is deleted"} {
		if !strings.Contains(RenderFoundingEmail(p), want) {
			t.Errorf("missing %q", want)
		}
	}
	in := RenderFoundingInApp(FoundingParams{End: "2027-11-15", Subscribed: true, ExtendedFrom: "2027-11-01"})
	if !strings.Contains(in.Body, "subscription continues") || !strings.Contains(in.Body, "moved the date from November 1, 2027") || !strings.Contains(in.Title, "November 15, 2027") {
		t.Errorf("%+v", in)
	}
}

// ---- renewal reminder (a stub until the billing item wires a source)

func TestRenewalJobWithoutASourceSendsNothing(t *testing.T) {
	st, err := (&RenewalJob{}).RunOnce(context.Background())
	if err != nil || st != (DatedStats{}) {
		t.Errorf("%+v %v", st, err)
	}
}

func TestRenewalKindIsPerPeriod(t *testing.T) {
	if RenewalKind(day("2027-12-01")) == RenewalKind(day("2028-12-01")) || RenewalKind(day("2027-12-01")) != "renewal_20271201" {
		t.Error("the reminder is not one per renewal period")
	}
}

func TestRenewalReminderCopy(t *testing.T) {
	p := RenewalParams{RenewsOn: "2027-12-01", PriceLabel: "$34.99 a year"}
	body := RenderRenewalEmail(p)
	for _, want := range []string{"December 1, 2027", "$34.99 a year", PlanURL, "nothing is deleted"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(RenewalSubject(p), "December 1, 2027") {
		t.Error(RenewalSubject(p))
	}
}

// The reminder links to the Plan card in Settings, never to a billing-portal session: those expire and are a bearer link if the email is forwarded.
func TestRenewalReminderLinksToThePlanCard(t *testing.T) {
	body := RenderRenewalEmail(RenewalParams{RenewsOn: "2027-12-01", PriceLabel: "$34.99 a year"})
	if !strings.Contains(body, "https://rivolt.dev/settings#plan") || strings.Contains(body, "stripe.com") {
		t.Errorf("cancel link:\n%s", body)
	}
}
