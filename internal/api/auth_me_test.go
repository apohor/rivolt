package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/entitlements"
)

func newMeAuth(t *testing.T) *auth.Service {
	t.Helper()
	s, err := auth.New(auth.Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    func(string) uuid.UUID { return uuid.New() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func meGet(t *testing.T, svc *auth.Service, mutate func(*http.Request) *http.Request) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	if mutate != nil {
		req = mutate(req)
	}
	rec := httptest.NewRecorder()
	handleMeEnriched(svc, nil, false)(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// A signed-out visitor with no credential is not an error.
func TestAuthMe_SignedOutIs200Unauthenticated(t *testing.T) {
	code, body := meGet(t, newMeAuth(t), nil)
	if code != http.StatusOK || body["authenticated"] != false {
		t.Fatalf("code %d body %v, want 200 {authenticated:false}", code, body)
	}
	if _, has := body["user_id"]; has {
		t.Fatalf("signed-out body must not carry identity: %v", body)
	}
}

// An expired or invalid session (a credential was presented but did
// not resolve) keeps the 401 the SPA relies on.
func TestAuthMe_PresentedButUnresolvedCredentialStays401(t *testing.T) {
	code, _ := meGet(t, newMeAuth(t), func(r *http.Request) *http.Request {
		r.AddCookie(&http.Cookie{Name: "rivolt_session", Value: "stale"})
		return r
	})
	if code != http.StatusUnauthorized {
		t.Errorf("cookie rivolt_session unresolved: code %d, want 401", code)
	}
	// A Kratos cookie is a credential only where a resolver can turn it into a user (RIVOLT_ACCEPT_KRATOS_COOKIE). Default off, so
	// it is ignored: signed out, not a 401.
	code, body := meGet(t, newMeAuth(t), func(r *http.Request) *http.Request {
		r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "stale"})
		return r
	})
	if code != http.StatusOK || body["authenticated"] != false {
		t.Errorf("a Kratos cookie with the resolver off: code %d body %v, want 200 {authenticated:false}", code, body)
	}
	// With the resolver on, a stale Kratos cookie is a presented-but-unresolved credential: 401.
	s, err := auth.New(auth.Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    func(string) uuid.UUID { return uuid.New() },
		KratosResolver: func(context.Context, string) (uuid.UUID, bool, error) {
			return uuid.Nil, false, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, _ = meGet(t, s, func(r *http.Request) *http.Request {
		r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "stale"})
		return r
	})
	if code != http.StatusUnauthorized {
		t.Errorf("a stale Kratos cookie with the resolver on: code %d, want 401", code)
	}
}

func TestAuthMe_SignedInIsUnchangedPlusAuthenticated(t *testing.T) {
	uid := uuid.New()
	code, body := meGet(t, newMeAuth(t), func(r *http.Request) *http.Request {
		return r.WithContext(auth.WithUser(r.Context(), uid))
	})
	if code != http.StatusOK || body["authenticated"] != true || body["user_id"] != uid.String() || body["role"] != "user" {
		t.Fatalf("code %d body %v", code, body)
	}
	if _, ok := body["onboarding_completed"]; !ok {
		t.Fatalf("existing fields must remain: %v", body)
	}
}

// The signed-out 200 must not be cacheable.
func TestAuthMe_SignedOutResponseIsNoStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	rec := httptest.NewRecorder()
	handleMeEnriched(newMeAuth(t), nil, false)(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code %d Cache-Control %q, want 200 no-store", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

// `passkeys` is on /api/auth/me only where the server offers passkeys, and absent otherwise, so Settings shows the card only where
// it can work.
func TestAuthMe_PasskeysFlag(t *testing.T) {
	get := func(on bool) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		req = req.WithContext(auth.WithUser(req.Context(), uuid.New()))
		rec := httptest.NewRecorder()
		handleMeEnrichedWith(newMeAuth(t), nil, false, nil, on)(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body
	}
	if v, has := get(false)["passkeys"]; has {
		t.Errorf("passkeys off: key present (%v); absent means off", v)
	}
	if get(true)["passkeys"] != true {
		t.Error("passkeys on: want passkeys:true")
	}
}

type meNoDB struct{}

func (meNoDB) Connect(context.Context) (driver.Conn, error) { return nil, errors.New("no database") }
func (meNoDB) Driver() driver.Driver                        { return nil }

// meAs is GET /api/auth/me as uid, through the plan-aware handler.
func meAs(t *testing.T, uid uuid.UUID, e *entitlements.Service) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil).WithContext(auth.WithUser(context.Background(), uid))
	rec := httptest.NewRecorder()
	handleMeEnrichedPlan(newMeAuth(t), nil, false, nil, false, e)(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return body
}

// The plan fields of /api/auth/me (Rivolt Pro): billing off is Pro for everyone with billing_enabled false, so clients hide every paywall;
// the demo account is always Pro; a lookup failure leaves the fields out entirely rather than guessing; no service, no fields.
func TestAuthMePlanFields(t *testing.T) {
	uid := uuid.New()
	off := meAs(t, uid, entitlements.New(nil, entitlements.Config{}))
	if off["plan"] != "pro" || off["plan_source"] != "billing_off" || off["billing_enabled"] != false || off["plan_period_end"] != nil || off["plan_trial_ends"] != nil {
		t.Errorf("billing off: %v", off)
	}
	for _, k := range []string{"plan", "plan_source", "plan_period_end", "plan_trial_ends", "plan_cancels_at", "plan_grace_ends", "plan_ended_at", "plan_ended_source", "billing_enabled", "history_days"} {
		if _, has := off[k]; !has {
			t.Errorf("billing off: %q missing (null is fine, absent is not)", k)
		}
	}

	db := sql.OpenDB(meNoDB{})
	defer db.Close()
	demo := uuid.New()
	on := entitlements.New(db, entitlements.Config{Billing: true, Demo: demo})
	if b := meAs(t, demo, on); b["plan"] != "pro" || b["plan_source"] != "demo" || b["billing_enabled"] != true {
		t.Errorf("demo: %v", b)
	}
	b := meAs(t, uid, on) // the lookup fails
	for _, k := range []string{"plan", "plan_source", "plan_period_end", "plan_trial_ends", "plan_cancels_at", "plan_grace_ends", "plan_ended_at", "plan_ended_source", "billing_enabled", "history_days"} {
		if _, has := b[k]; has {
			t.Errorf("a failed lookup still sent %q: %v", k, b)
		}
	}
	if b["authenticated"] != true {
		t.Errorf("the rest of the answer must survive: %v", b)
	}
	if n := meAs(t, uid, nil); n["plan"] != nil {
		t.Errorf("no service, plan fields present: %v", n)
	}
}

// A free account: plan free, no source, no end, billing on.
func TestPlanFieldsForAFreeAccount(t *testing.T) {
	pf := planFromState(entitlements.State{}, true) // never had Pro
	b, _ := json.Marshal(pf)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["plan"] != "free" || m["plan_source"] != nil || m["plan_period_end"] != nil || m["plan_trial_ends"] != nil || m["plan_ended_at"] != nil || m["plan_ended_source"] != nil || m["billing_enabled"] != true || m["history_days"] != float64(90) {
		t.Errorf("%s", b)
	}
}
