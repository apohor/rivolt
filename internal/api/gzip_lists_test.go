package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// The list routes are wrapped in middleware.Compress(5, "application/json"):
// a client that accepts gzip gets it, one that doesn't gets plain JSON.
func TestListRoutesGzipJSON(t *testing.T) {
	t.Parallel()
	body := `[` + strings.Repeat(`{"a":"xxxxxxxxxxxxxxxxxxxx"},`, 500) + `{}]`
	r := chi.NewRouter()
	r.With(middleware.Compress(5, "application/json")).Get("/x", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []byte(body))
	})
	get := func(ae string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/x", nil)
		if ae != "" {
			req.Header.Set("Accept-Encoding", ae)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := get("gzip"); w.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("gzip client got Content-Encoding %q", w.Header().Get("Content-Encoding"))
	} else if zr, err := gzip.NewReader(w.Body); err != nil {
		t.Errorf("not gzip: %v", err)
	} else if b, _ := io.ReadAll(zr); len(b) == 0 {
		t.Error("empty gunzipped body")
	}
	if w := get(""); w.Header().Get("Content-Encoding") != "" {
		t.Errorf("plain client got Content-Encoding %q", w.Header().Get("Content-Encoding"))
	}
}
