package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/signuprequests"
)

// Every reason behind ErrTokenInvalid has its own result label; anything that
// is not a token reason is an error.
func TestSignupTokenResult(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{signuprequests.ErrTokenNotFound, "not_found"},
		{signuprequests.ErrTokenNotApproved, "not_approved"},
		{signuprequests.ErrTokenUsed, "used"},
		{signuprequests.ErrTokenExpired, "expired"},
		{signuprequests.ErrTokenInvalid, "not_found"},
		{fmt.Errorf("wrapped: %w", signuprequests.ErrTokenUsed), "used"},
		{fmt.Errorf("lookup: %w", context.Canceled), "canceled"},
		{errors.New("sql: connection refused"), "error"},
	} {
		if got := signupTokenResult(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

// A database error is a 5xx, counted as error and logged without the token.
func TestSignupTokenLookupDatabaseErrorIs5xx(t *testing.T) {
	d, err := sql.Open("signup-err-driver", "")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	m := metrics.New()
	h := handleSignupTokenLookup(signuprequests.New(d), m, logger)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("token", "secret-token-value")
	req := httptest.NewRequest("GET", "/api/signup/token/secret-token-value", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if got := testutil.ToFloat64(m.SignupTokenLookupsTotal.WithLabelValues("get", "error")); got != 1 {
		t.Errorf("error count %v, want 1", got)
	}
	if !strings.Contains(buf.String(), "signup token lookup failed") {
		t.Errorf("no log line: %q", buf.String())
	}
	if strings.Contains(buf.String(), "secret-token-value") {
		t.Errorf("log carries the token: %q", buf.String())
	}
}
