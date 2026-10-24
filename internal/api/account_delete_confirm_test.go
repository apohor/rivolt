package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/sessions"
)

func deleteReq(body, contentType string, mut func(*http.Request) *http.Request) *http.Request {
	r := httptest.NewRequest(http.MethodDelete, "https://rivolt.dev/api/data/account", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	r = r.WithContext(auth.WithSession(auth.WithUser(r.Context(), uuid.New()), auth.SessionRef{ID: uuid.New(), Kind: sessions.KindWeb}))
	if mut != nil {
		r = mut(r)
	}
	return r
}

// A deletion request must be a deliberate one: a JSON body that says "delete my account", from a browser session that is
// not an admin viewing as the user, not the app's token and not cross-site.
func TestAccountDeleteRefusal(t *testing.T) {
	const ok = `{"confirm":"delete my account"}`
	for _, c := range []struct {
		name   string
		req    *http.Request
		code   int
		reason string
	}{
		{"the confirmation, as JSON, from a browser", deleteReq(ok, "application/json", nil), 0, ""},
		{"with a charset", deleteReq(ok, "application/json; charset=utf-8", nil), 0, ""},
		{"a bare DELETE", deleteReq("", "", nil), 415, "json_required"},
		{"no body but JSON", deleteReq("", "application/json", nil), 400, "confirmation_required"},
		{"a form post", deleteReq("confirm=delete+my+account", "application/x-www-form-urlencoded", nil), 415, "json_required"},
		{"text/plain (the cross-site form encoding)", deleteReq(ok, "text/plain", nil), 415, "json_required"},
		{"the wrong words", deleteReq(`{"confirm":"yes"}`, "application/json", nil), 400, "confirmation_required"},
		{"different case", deleteReq(`{"confirm":"Delete my account"}`, "application/json", nil), 400, "confirmation_required"},
		{"an unknown field", deleteReq(`{"confirm":"delete my account","x":1}`, "application/json", nil), 400, "confirmation_required"},
		{"something after the object", deleteReq(ok+` {}`, "application/json", nil), 400, "confirmation_required"},
		{"not JSON", deleteReq("delete my account", "application/json", nil), 400, "confirmation_required"},
		{"a cross-site browser request", deleteReq(ok, "application/json", func(r *http.Request) *http.Request {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			return r
		}), 403, "cross_site_request"},
		{"same-origin is fine", deleteReq(ok, "application/json", func(r *http.Request) *http.Request {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			return r
		}), 0, ""},
		{"an admin viewing as the user", deleteReq(ok, "application/json", func(r *http.Request) *http.Request {
			return r.WithContext(auth.WithImpersonation(r.Context(), uuid.New(), uuid.New()))
		}), 403, "not_available_while_viewing_as_another_user"},
		{"the app's token", deleteReq(ok, "application/json", func(r *http.Request) *http.Request {
			return r.WithContext(auth.WithSession(r.Context(), auth.SessionRef{ID: uuid.New(), Kind: sessions.KindApp}))
		}), 403, "not_available_from_the_app"},
	} {
		code, reason := accountDeleteRefusal(c.req)
		if code != c.code || reason != c.reason {
			t.Errorf("%s: %d %q, want %d %q", c.name, code, reason, c.code, c.reason)
		}
	}
}

// Through the handler: the refusals come before anything is looked up (no database is needed for them), and a valid
// request reaches the database check.
func TestAccountDeleteHandlerChecksBeforeTheDatabase(t *testing.T) {
	h := handleDataAccountDelete(Deps{}) // no database
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, deleteReq("", "", nil))
	if rec.Code != http.StatusUnsupportedMediaType || !strings.Contains(rec.Body.String(), "json_required") {
		t.Errorf("a bare DELETE: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, deleteReq(`{"confirm":"delete my account"}`, "application/json", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a valid request with no database: %d, want 503", rec.Code)
	}
	// Signed out: 401, whatever the body.
	req := httptest.NewRequest(http.MethodDelete, "/api/data/account", strings.NewReader(`{"confirm":"delete my account"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", rec.Code)
	}
}

// The app's token never deletes an account: the write list refuses the route before the handler, and the handler refuses it
// too (people are sent to the web page to delete).
func TestAccountDeleteIsNotOnTheAppWriteList(t *testing.T) {
	if appWrites["DELETE /api/data/account"] {
		t.Fatal("DELETE /api/data/account is on the app write allowlist")
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "https://rivolt.dev/api/data/account", strings.NewReader(`{"confirm":"delete my account"}`))
	req.Header.Set("Content-Type", "application/json")
	asSession(appWriteAllowlistMW(ok), sessions.KindApp).ServeHTTP(rec, req)
	if !denied(rec) {
		t.Errorf("an app session's DELETE /api/data/account: %d %s, want the app-write refusal", rec.Code, rec.Body.String())
	}
}

// The audit label is stable for one account and key, differs across accounts and keys, and never contains the id.
func TestAuditUserHash(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	k1, k2 := []byte("key-one"), []byte("key-two")
	h := auditUserHash(k1, a)
	if h != auditUserHash(k1, a) || h == auditUserHash(k1, b) || h == auditUserHash(k2, a) {
		t.Errorf("hash %s is not stable per (key, user) and distinct otherwise", h)
	}
	if len(h) != 16 || strings.Contains(h, strings.ReplaceAll(a.String(), "-", "")[:8]) {
		t.Errorf("hash %q: want 16 hex chars that are not the id", h)
	}
	first, second := auditUserHash(nil, a), auditUserHash(nil, a)
	if first == "" || first != second {
		t.Error("the keyless fallback is empty or unstable within a process")
	}
}
