package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

// The Kratos session cookie is not accepted unless RIVOLT_ACCEPT_KRATOS_COOKIE=true.
func TestKratosCookieResolverIsOffByDefault(t *testing.T) {
	called := false
	resolve := func(context.Context, string) (uuid.UUID, bool, error) { called = true; return uuid.New(), true, nil }
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "RIVOLT_ACCEPT_KRATOS_COOKIE" {
				return v
			}
			return ""
		}
	}
	for _, off := range []string{"", "false", "0", "no", "yes", "1"} {
		if kratosCookieResolver(env(off), resolve) != nil {
			t.Errorf("RIVOLT_ACCEPT_KRATOS_COOKIE=%q must leave the resolver off", off)
		}
	}
	for _, on := range []string{"true", "TRUE", " true "} {
		if kratosCookieResolver(env(on), resolve) == nil {
			t.Errorf("RIVOLT_ACCEPT_KRATOS_COOKIE=%q must turn it on", on)
		}
	}
	if called {
		t.Error("choosing the resolver must not call it")
	}
}

// What it means for a request: with the resolver off (the default) a request carrying only a Kratos cookie is anonymous, even
// when Kratos would call the session valid; turned on, it is signed in.
func TestKratosCookieOnlyRequestIsAnonymousByDefault(t *testing.T) {
	valid := func(context.Context, string) (uuid.UUID, bool, error) { return uuid.New(), true, nil }
	signedIn := func(resolver func(context.Context, string) (uuid.UUID, bool, error)) bool {
		svc, err := auth.New(auth.Config{UserIDFor: func(string) uuid.UUID { return uuid.New() }, KratosResolver: resolver})
		if err != nil {
			t.Fatal(err)
		}
		var got bool
		h := svc.Middleware(httpHandler(func(ctx context.Context) { _, got = auth.UserFromContext(ctx) }))
		h.ServeHTTP(newRec(), newReqWithCookie("ory_kratos_session", "valid"))
		return got
	}
	if signedIn(kratosCookieResolver(func(string) string { return "" }, valid)) {
		t.Error("a request carrying only a Kratos cookie must be anonymous by default")
	}
	if !signedIn(kratosCookieResolver(func(string) string { return "true" }, valid)) {
		t.Error("with RIVOLT_ACCEPT_KRATOS_COOKIE=true a valid Kratos cookie signs the request in")
	}
}

func httpHandler(f func(ctx context.Context)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f(r.Context()) })
}
func newRec() *httptest.ResponseRecorder { return httptest.NewRecorder() }
func newReqWithCookie(name, val string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
	r.AddCookie(&http.Cookie{Name: name, Value: val})
	return r
}
