package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/trips"
)

// The saved-trip routes answer JSON {error, class}, and a trip over the cap is refused with 413 before any database call.
func TestSavedTripBodyTooLargeAnswersJSON(t *testing.T) {
	big := `{"name":"x","inputs":{"a":1},"plan":{"pad":"` + strings.Repeat("a", savedTripMaxBytes) + `"}}`
	for name, call := range map[string]func(*httptest.ResponseRecorder, *http.Request){
		"create": func(rec *httptest.ResponseRecorder, req *http.Request) {
			handleSavedTripCreate(&trips.Store{}, nil, uuid.Nil)(rec, req)
		},
		"update": func(rec *httptest.ResponseRecorder, req *http.Request) {
			rc := chi.NewRouteContext()
			rc.URLParams.Add("id", uuid.NewString())
			handleSavedTripUpdate(&trips.Store{}, nil, uuid.Nil)(rec, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc)))
		},
	} {
		rec := httptest.NewRecorder()
		call(rec, httptest.NewRequest(http.MethodPost, "/api/trips/saved", strings.NewReader(big)))
		var out struct{ Error, Class string }
		if rec.Code != http.StatusRequestEntityTooLarge || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Class != "too_large" || out.Error == "" {
			t.Errorf("%s: %d %s, want 413 {error, class:too_large}", name, rec.Code, rec.Body.String())
		}
	}
}

// Just under the cap passes the size check (it fails later, at the missing database, which is what this stops short of).
func TestSavedTripSizeCheckBoundary(t *testing.T) {
	under := savedTripBody{Name: "n", Inputs: json.RawMessage(`{}`), Plan: json.RawMessage(`"` + strings.Repeat("a", savedTripMaxBytes-20) + `"`)}
	if savedTripTooLarge(httptest.NewRecorder(), "n", under) {
		t.Error("a trip under the cap was refused")
	}
	over := savedTripBody{Name: "n", Inputs: json.RawMessage(`{}`), Plan: json.RawMessage(`"` + strings.Repeat("a", savedTripMaxBytes) + `"`)}
	if !savedTripTooLarge(httptest.NewRecorder(), "n", over) {
		t.Error("a trip over the cap was accepted")
	}
}

func TestSavedTripErrorsAreJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	handleSavedTripCreate(&trips.Store{}, nil, uuid.Nil)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/saved", strings.NewReader(`{"name":" ","inputs":{}}`)))
	var out struct{ Error, Class string }
	if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Class != "invalid" || out.Error != "name required" {
		t.Errorf("%d %s, want 400 {error:\"name required\", class:invalid}", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handleSavedTripCreate(nil, nil, uuid.Nil)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/saved", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"class":"no_user"`) {
		t.Errorf("no user: %d %s", rec.Code, rec.Body.String())
	}
}

// Saves, edits and links are limited to 30 a minute per user, through the real router (no store is wired, so each call that gets past
// the limiter ends at "no user"; the point is where the 429s start).
func TestSavedTripWritesAreLimitedPerUser(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/trips/saved"},
		{http.MethodPut, "/api/trips/saved/" + id},
	} {
		h := asSession(New(Deps{Auth: svc}), sessions.KindApp) // one user per route
		passed, limited := 0, 0
		for i := 0; i < 60; i++ {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(c.method, "https://rivolt.dev"+c.path, strings.NewReader(`{}`))
			req.RemoteAddr = "203.0.113.9:4000"
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusTooManyRequests {
				limited++
			} else {
				passed++
			}
		}
		if passed < 30 || passed > 32 || limited == 0 {
			t.Errorf("%s %s: %d passed and %d limited of 60, want about 30 then limited", c.method, c.path, passed, limited)
		}
	}
}
