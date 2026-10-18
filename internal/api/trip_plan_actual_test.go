package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/trips"
)

type fakePlanLinks struct{ rows []planLink }

func (f *fakePlanLinks) List(_ context.Context, _, _ uuid.UUID, day int) ([]planLink, error) {
	var out []planLink
	for _, r := range f.rows {
		if r.Day == day {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakePlanLinks) Link(_ context.Context, _, _ uuid.UUID, l planLink) error {
	for i := range f.rows {
		if f.rows[i].Day == l.Day && f.rows[i].Status == "linked" && f.rows[i].TripID != l.TripID {
			f.rows[i].Status = "unlinked"
		}
	}
	for i := range f.rows {
		if f.rows[i].Day == l.Day && f.rows[i].TripID == l.TripID {
			l.LinkedAt = time.Now()
			f.rows[i] = l
			return nil
		}
	}
	l.LinkedAt = time.Now()
	f.rows = append(f.rows, l)
	return nil
}
func (f *fakePlanLinks) Unlink(_ context.Context, _, _ uuid.UUID, l planLink) error {
	for i := range f.rows {
		if f.rows[i].Day == l.Day && f.rows[i].TripID == l.TripID {
			f.rows[i].Status = "unlinked"
			return nil
		}
	}
	l.Status = "unlinked"
	f.rows = append(f.rows, l)
	return nil
}

var paSaved = &trips.SavedTrip{
	ID: uuid.New(), Name: "LA to Vegas",
	Inputs: json.RawMessage(`{"v":"inputs-now"}`), Plan: json.RawMessage(`{"v":"plan-now"}`),
	CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
}

func newFakePA(cands []ledger.PlanCandidate) (*planActualAPI, *fakePlanLinks, *[]string) {
	links := &fakePlanLinks{}
	var compared []string // which plan JSON each compare saw
	a := &planActualAPI{
		getTrip: func(_ context.Context, _, id uuid.UUID) (*trips.SavedTrip, error) {
			if id != paSaved.ID {
				return nil, trips.ErrNotFound
			}
			return paSaved, nil
		},
		links: links,
		input: func(context.Context, uuid.UUID, time.Time, time.Time) (ledger.Input, error) {
			return ledger.Input{}, nil
		},
		parse: func(inputs, plan json.RawMessage, _ time.Time) (ledger.Plan, error) {
			compared = append(compared, "parse:"+string(plan))
			return ledger.Plan{}, nil
		},
		match: func(ledger.Plan, ledger.Input) []ledger.PlanCandidate { return cands },
		compare: func(_ ledger.Plan, _ ledger.Input, tripID string) (ledger.PlanComparison, bool) {
			compared = append(compared, "compare:"+tripID)
			return ledger.PlanComparison{Currency: "USD"}, tripID != "trip_gone"
		},
		window: func(ledger.Plan) (time.Time, time.Time) {
			return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		},
		now: time.Now,
	}
	return a, links, &compared
}

func paServe(a *planActualAPI, method, path, body string) (int, planActualResponse) {
	r := chi.NewRouter()
	uid := uuid.New()
	r.Get("/trips/saved/{id}/actual", func(w http.ResponseWriter, req *http.Request) { a.actual(uid, w, req) })
	r.Post("/trips/saved/{id}/link", func(w http.ResponseWriter, req *http.Request) { a.link(uid, w, req) })
	r.Post("/trips/saved/{id}/unlink", func(w http.ResponseWriter, req *http.Request) { a.unlink(uid, w, req) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
	var out planActualResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func pathFor(id uuid.UUID, tail string) string { return "/trips/saved/" + id.String() + "/" + tail }

func cand(id string, conf float64, band string) ledger.PlanCandidate {
	st := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	return ledger.PlanCandidate{TripID: id, StartedAt: st, EndedAt: st.Add(30 * time.Hour), Confidence: conf, Band: band}
}

func TestPlanActual_NoneSuggestedAndMedium(t *testing.T) {
	a, _, _ := newFakePA(nil)
	code, out := paServe(a, "GET", pathFor(paSaved.ID, "actual"), "")
	if code != 200 || out.Status != "none" || out.Comparison != nil || out.Link != nil || out.Candidates == nil || len(out.Candidates) != 0 {
		t.Fatalf("none: %d %+v", code, out)
	}
	a, _, _ = newFakePA([]ledger.PlanCandidate{cand("trip_a", 0.91, "high"), cand("trip_b", 0.6, "medium")})
	_, out = paServe(a, "GET", pathFor(paSaved.ID, "actual"), "")
	if out.Status != "suggested" || out.Comparison == nil || out.Comparison.PlanFrom != "current" || len(out.Candidates) != 2 || out.Link != nil {
		t.Fatalf("suggested: %+v", out)
	}
	a, _, _ = newFakePA([]ledger.PlanCandidate{cand("trip_b", 0.6, "medium")})
	_, out = paServe(a, "GET", pathFor(paSaved.ID, "actual"), "")
	if out.Status != "none" || out.Comparison != nil || len(out.Candidates) != 1 {
		t.Fatalf("medium listed without a preview: %+v", out)
	}
}

func TestPlanActual_LinkSnapshotsAndComparesTheSnapshot(t *testing.T) {
	a, links, _ := newFakePA([]ledger.PlanCandidate{cand("trip_a", 0.91, "high")})
	if code, _ := paServe(a, "POST", pathFor(paSaved.ID, "link"), `{"trip_id":"trip_nope"}`); code != http.StatusConflict {
		t.Fatalf("non-candidate = %d, want 409", code)
	}
	if code, _ := paServe(a, "POST", pathFor(paSaved.ID, "link"), `{}`); code != http.StatusBadRequest {
		t.Fatalf("missing trip_id = %d, want 400", code)
	}
	code, out := paServe(a, "POST", pathFor(paSaved.ID, "link"), `{"trip_id":"trip_a"}`)
	if code != 200 || out.Status != "linked" || out.Link == nil || out.Link.TripID != "trip_a" || out.Link.Band != "high" || out.Comparison == nil {
		t.Fatalf("link: %d %+v", code, out)
	}
	if len(links.rows) != 1 || string(links.rows[0].Snapshot.Plan) != `{"v":"plan-now"}` || string(links.rows[0].Snapshot.Inputs) != `{"v":"inputs-now"}` {
		t.Fatalf("snapshot not stored: %+v", links.rows)
	}
	// Re-planning the saved trip afterwards must not change what the link compares.
	paSaved2 := *paSaved
	paSaved2.Plan = json.RawMessage(`{"v":"plan-replanned"}`)
	a.getTrip = func(context.Context, uuid.UUID, uuid.UUID) (*trips.SavedTrip, error) { return &paSaved2, nil }
	var parsed []string
	a.parse = func(_, plan json.RawMessage, _ time.Time) (ledger.Plan, error) {
		parsed = append(parsed, string(plan))
		return ledger.Plan{}, nil
	}
	_, out = paServe(a, "GET", pathFor(paSaved.ID, "actual"), "")
	foundSnapshot := false
	for _, p := range parsed {
		if p == `{"v":"plan-now"}` {
			foundSnapshot = true
		}
	}
	if out.Status != "linked" || !foundSnapshot || out.Comparison == nil || out.Comparison.PlanFrom != "snapshot" {
		t.Fatalf("linked comparison must parse the snapshot plan: %v %+v", parsed, out)
	}
}

func TestPlanActual_UnlinkExcludesAndNeverResuggests(t *testing.T) {
	a, links, _ := newFakePA([]ledger.PlanCandidate{cand("trip_a", 0.91, "high"), cand("trip_b", 0.6, "medium")})
	paServe(a, "POST", pathFor(paSaved.ID, "link"), `{"trip_id":"trip_a"}`)
	_, out := paServe(a, "POST", pathFor(paSaved.ID, "unlink"), "")
	if out.Status == "linked" || out.Link != nil {
		t.Fatalf("still linked: %+v", out)
	}
	for _, c := range out.Candidates {
		if c.TripID == "trip_a" {
			t.Fatal("an unlinked outing must not be re-suggested")
		}
	}
	if out.Status != "none" {
		t.Fatalf("after unlink the remaining candidate is medium: %+v", out)
	}
	// Linking another outing replaces the first (one linked row).
	paServe(a, "POST", pathFor(paSaved.ID, "link"), `{"trip_id":"trip_b"}`)
	n := 0
	for _, r := range links.rows {
		if r.Status == "linked" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("linked rows = %d, want 1", n)
	}
}

// No body with nothing linked is a no-op: a double-click on Unlink
// must not dismiss the next candidate.
func TestPlanActual_UnlinkWithNothingLinkedIsANoOp(t *testing.T) {
	a, links, _ := newFakePA([]ledger.PlanCandidate{cand("trip_a", 0.91, "high")})
	code, out := paServe(a, "POST", pathFor(paSaved.ID, "unlink"), "")
	if code != 200 || out.Status != "suggested" || len(links.rows) != 0 {
		t.Fatalf("no-op unlink wrote or changed state: %d %+v rows=%+v", code, out, links.rows)
	}
	// Link, then unlink twice: the second is a no-op, the suggestion
	// (trip_a) stays unlinked and nothing else is dismissed.
	a2, links2, _ := newFakePA([]ledger.PlanCandidate{cand("trip_a", 0.91, "high"), cand("trip_b", 0.9, "high")})
	paServe(a2, "POST", pathFor(paSaved.ID, "link"), `{"trip_id":"trip_a"}`)
	paServe(a2, "POST", pathFor(paSaved.ID, "unlink"), "")
	before := len(links2.rows)
	_, out = paServe(a2, "POST", pathFor(paSaved.ID, "unlink"), "")
	if len(links2.rows) != before {
		t.Fatalf("second unlink wrote rows: %+v", links2.rows)
	}
	if out.Status != "suggested" || out.Comparison == nil {
		t.Fatalf("next best high candidate should be the suggestion: %+v", out)
	}
}

func TestPlanActual_RejectACandidateByTripID(t *testing.T) {
	a, links, _ := newFakePA([]ledger.PlanCandidate{cand("trip_a", 0.91, "high"), cand("trip_b", 0.85, "high"), cand("trip_m", 0.6, "medium")})
	_, out := paServe(a, "GET", pathFor(paSaved.ID, "actual"), "")
	if out.Status != "suggested" || out.Candidates[0].TripID != "trip_a" {
		t.Fatalf("setup: %+v", out)
	}
	// Reject the suggested trip.
	code, out := paServe(a, "POST", pathFor(paSaved.ID, "unlink"), `{"trip_id":"trip_a"}`)
	if code != 200 || len(links.rows) != 1 || links.rows[0].Status != "unlinked" || links.rows[0].TripID != "trip_a" {
		t.Fatalf("reject must record an unlinked row: %d %+v", code, links.rows)
	}
	if string(links.rows[0].Snapshot.Plan) != `{"v":"plan-now"}` {
		t.Fatalf("rejected row needs a snapshot: %+v", links.rows[0])
	}
	for _, c := range out.Candidates {
		if c.TripID == "trip_a" {
			t.Fatal("rejected candidate still listed")
		}
	}
	if out.Status != "suggested" || out.Comparison == nil {
		t.Fatalf("the next high candidate becomes the suggestion: %+v", out)
	}
	// A listed medium candidate can be rejected too.
	if code, _ := paServe(a, "POST", pathFor(paSaved.ID, "unlink"), `{"trip_id":"trip_m"}`); code != 200 {
		t.Fatalf("reject medium = %d", code)
	}
	// Not a candidate: 409, nothing written.
	n := len(links.rows)
	if code, _ := paServe(a, "POST", pathFor(paSaved.ID, "unlink"), `{"trip_id":"trip_zzz"}`); code != http.StatusConflict || len(links.rows) != n {
		t.Fatalf("non-candidate reject = %d rows %d->%d", code, n, len(links.rows))
	}
	// A rejected outing can be linked again (changed mind): flips back.
	a.match = func(ledger.Plan, ledger.Input) []ledger.PlanCandidate {
		return []ledger.PlanCandidate{cand("trip_a", 0.91, "high")}
	}
	code, out = paServe(a, "POST", pathFor(paSaved.ID, "link"), `{"trip_id":"trip_a"}`)
	if code != 200 || out.Status != "linked" {
		t.Fatalf("re-link of a rejected outing: %d %+v", code, out)
	}
}

func TestPlanActual_StaleLinkRefoundByTimeOverlap(t *testing.T) {
	a, links, _ := newFakePA([]ledger.PlanCandidate{cand("trip_new", 0.9, "high")})
	st := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	conf := 0.9
	links.rows = []planLink{{TripID: "trip_gone", Status: "linked", Confidence: &conf, StartedAt: st, EndedAt: st.Add(30 * time.Hour),
		Snapshot: planSnapshot{Inputs: paSaved.Inputs, Plan: paSaved.Plan}}}
	_, out := paServe(a, "GET", pathFor(paSaved.ID, "actual"), "")
	if out.Status != "linked" || out.Link == nil || !out.Link.LinkStale || out.Link.TripID != "trip_new" || out.Comparison == nil {
		t.Fatalf("stale link: %+v", out)
	}
}

// A linked outing that can't be rebuilt and can't be re-found is
// reported as stale with no comparison.
func TestPlanActual_UnrecoverableLinkIsStaleWithoutComparison(t *testing.T) {
	a, links, _ := newFakePA(nil) // nothing to re-find
	st := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	conf := 0.9
	links.rows = []planLink{{TripID: "trip_gone", Status: "linked", Confidence: &conf, StartedAt: st, EndedAt: st.Add(time.Hour),
		Snapshot: planSnapshot{Inputs: paSaved.Inputs, Plan: paSaved.Plan}}}
	_, out := paServe(a, "GET", pathFor(paSaved.ID, "actual"), "")
	if out.Status != "linked" || out.Link == nil || !out.Link.LinkStale || out.Comparison != nil {
		t.Fatalf("unrecoverable: %+v", out)
	}
}

func TestPlanActual_NotFoundAndUnparsablePlan(t *testing.T) {
	a, _, _ := newFakePA(nil)
	if code, _ := paServe(a, "GET", pathFor(uuid.New(), "actual"), ""); code != http.StatusNotFound {
		t.Fatalf("someone else's trip = %d, want 404", code)
	}
	if code, _ := paServe(a, "GET", "/trips/saved/not-a-uuid/actual", ""); code != http.StatusNotFound {
		t.Fatalf("bad id = %d, want 404", code)
	}
	a.parse = func(json.RawMessage, json.RawMessage, time.Time) (ledger.Plan, error) {
		return ledger.Plan{}, ledger.ErrPlanUnusable
	}
	code, out := paServe(a, "GET", pathFor(paSaved.ID, "actual"), "")
	if code != 200 || out.Status != "none" || out.Candidates == nil {
		t.Fatalf("unparsable plan: %d %+v", code, out)
	}
}
