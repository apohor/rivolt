package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/flags"
)

// The handler refuses everything that could turn the flag on for more than
// the named users, before it touches the store.
func TestFlagsLiveDestinationPut_Rejects(t *testing.T) {
	const id = "3f2b8c1e-5a4d-4e6f-9a0b-1c2d3e4f5a6b"
	cases := []struct {
		name, body string
		want       int
	}{
		{"all true", `{"all":true}`, http.StatusBadRequest},
		{"all false", `{"all":false,"users":["` + id + `"]}`, http.StatusBadRequest},
		{"all as user", `{"users":["all"]}`, http.StatusBadRequest},
		{"not a uuid", `{"users":["alice"]}`, http.StatusBadRequest},
		{"one bad of two", `{"users":["` + id + `","nope"]}`, http.StatusBadRequest},
		{"users not a list", `{"users":"` + id + `"}`, http.StatusBadRequest},
		{"bad json", `{`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/flags/live-destination", strings.NewReader(c.body))
			// The zero Store is never reached: validation fails first.
			handleFlagsLiveDestinationPut(&flags.Store{}).ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("got %d (%s), want %d", rec.Code, strings.TrimSpace(rec.Body.String()), c.want)
			}
		})
	}
}

func TestFlagsLiveDestinationPut_NilStore(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/flags/live-destination", strings.NewReader(`{"users":[]}`))
	handleFlagsLiveDestinationPut(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", rec.Code)
	}
}
