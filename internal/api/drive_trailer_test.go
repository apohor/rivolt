package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/drives"
)

type fakeTrailerStore struct {
	got   map[string]*bool
	known map[string]bool
	calls int
}

func (f *fakeTrailerStore) SetTrailerOverride(_ context.Context, id string, v *bool) (bool, error) {
	f.calls++
	if !f.known[id] {
		return false, nil
	}
	f.got[id] = v
	return true, nil
}

func patchTrailer(h http.HandlerFunc, id, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Patch("/drives/{id}/trailer", h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/drives/"+id+"/trailer", strings.NewReader(body)))
	return rec
}

func TestDriveTrailerPatch(t *testing.T) {
	st := &fakeTrailerStore{got: map[string]*bool{}, known: map[string]bool{"live_v_d_1": true}}
	h := handleDriveTrailerPatch(st)
	for _, c := range []struct {
		body string
		want string // the stored override: true false nil
	}{
		{`{"trailer": true}`, "true"},
		{`{"trailer": false}`, "false"},
		{`{"trailer": null}`, "nil"},
	} {
		rec := patchTrailer(h, "live_v_d_1", c.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.body, rec.Code, rec.Body.String())
		}
		if got := flagPtr(st.got["live_v_d_1"]); got != c.want {
			t.Errorf("%s stored %s, want %s", c.body, got, c.want)
		}
	}
	// What a client must not be able to do by accident: an empty body, another key, a string, a number, two keys.
	for _, bad := range []string{``, `{}`, `{"trailer": "yes"}`, `{"trailer": 1}`, `{"trailer": true, "x": 1}`, `{"towing": true}`, `[true]`, `{"trailer": true} {"trailer": false}`} {
		before := st.calls
		if rec := patchTrailer(h, "live_v_d_1", bad); rec.Code != http.StatusBadRequest || st.calls != before {
			t.Errorf("%q: %d (store called %d times)", bad, rec.Code, st.calls-before)
		}
	}
	// Not the user's drive: 404.
	if rec := patchTrailer(h, "live_other_d_9", `{"trailer": true}`); rec.Code != http.StatusNotFound {
		t.Errorf("another user's drive: %d", rec.Code)
	}
	// No store: 503, never a panic.
	if rec := patchTrailer(handleDriveTrailerPatch(nil), "x", `{"trailer": true}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no store: %d", rec.Code)
	}
}

func flagPtr(v *bool) string {
	if v == nil {
		return "nil"
	}
	if *v {
		return "true"
	}
	return "false"
}

func TestMergedRoundTripTrailer(t *testing.T) {
	yes, no := true, false
	mk := func(det, over *bool) rtDrive {
		return rtDrive{d: drives.Drive{Trailer: det, TrailerOverride: over, StartedAt: time.Now()}}
	}
	for _, c := range []struct {
		name  string
		chain []rtDrive
		want  string
	}{
		{"nothing known", []rtDrive{mk(nil, nil), mk(nil, nil)}, "nil"},
		{"one leg with a trailer", []rtDrive{mk(&no, nil), mk(&yes, nil)}, "true"},
		{"known none", []rtDrive{mk(&no, nil), mk(nil, nil)}, "false"},
		{"the user's correction wins over a recorded trailer", []rtDrive{mk(&yes, &no), mk(&yes, &no)}, "false"},
		{"the user's correction can add one", []rtDrive{mk(&no, &yes), mk(&no, nil)}, "true"},
	} {
		if got := flagPtr(mergedTrailer(c.chain)); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestEffectiveTrailerIsTheCorrectionElseTheRecording(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		d    drives.Drive
		want string
	}{
		{drives.Drive{}, "nil"},
		{drives.Drive{Trailer: &yes}, "true"},
		{drives.Drive{Trailer: &yes, TrailerOverride: &no}, "false"},
		{drives.Drive{Trailer: &no, TrailerOverride: &yes}, "true"},
		{drives.Drive{TrailerOverride: &yes}, "true"},
	} {
		if got := flagPtr(c.d.HasTrailer()); got != c.want {
			t.Errorf("%+v: %s, want %s", c.d, got, c.want)
		}
	}
}
