package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/sessions"
)

// accountDeleteConfirm is what a deletion request must say, as its JSON body: {"confirm":"delete my account"}. A bare DELETE
// (a script, a stray call, a leaked cookie replayed blind) is refused; the body is the second step after the page's own button.
const accountDeleteConfirm = "delete my account"

// accountDeleteRefusal checks a deletion request before anything is looked up or removed. It returns the status and a
// machine-readable reason, or 0 when the request may proceed. Order: the actor (not an admin viewing as the user, not the
// iPhone app's token: the app sends people to the web page to delete), then a cross-site browser request, then the form of
// the request (JSON, with the confirmation).
func accountDeleteRefusal(r *http.Request) (int, string) {
	if auth.IsImpersonating(r.Context()) {
		return http.StatusForbidden, "not_available_while_viewing_as_another_user"
	}
	if ref, ok := auth.SessionFromContext(r.Context()); ok && ref.Kind == sessions.KindApp {
		return http.StatusForbidden, "not_available_from_the_app"
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return http.StatusForbidden, "cross_site_request"
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return http.StatusUnsupportedMediaType, "json_required"
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Confirm != accountDeleteConfirm {
		return http.StatusBadRequest, "confirmation_required"
	}
	if _, err := dec.Token(); err != io.EOF {
		return http.StatusBadRequest, "confirmation_required" // anything after the object
	}
	return 0, ""
}

// accountActor is who is making the request, for the audit line: the session's kind ("web" or "app"), or "other" for a
// header or bypass sign-in that has no session.
func accountActor(r *http.Request) string {
	if ref, ok := auth.SessionFromContext(r.Context()); ok && ref.Kind != "" {
		return ref.Kind
	}
	return "other"
}

var (
	auditFallbackOnce sync.Once
	auditFallbackKey  []byte
)

// auditUserHash is a salted, one-way label for a user id in the log line of an account deletion: the account is gone, so the
// raw id would only be a way back to a person, but the same deleted account still reads as one event. HMAC-SHA256 under the
// server's own key (derived from the cookie secret), first 8 bytes. With no key configured it uses a random one for the
// process, so the label still cannot be reversed.
func auditUserHash(key []byte, uid uuid.UUID) string {
	if len(key) == 0 {
		auditFallbackOnce.Do(func() {
			auditFallbackKey = make([]byte, 32)
			_, _ = rand.Read(auditFallbackKey)
		})
		key = auditFallbackKey
	}
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte("rivolt/account-deleted/" + strings.ToLower(uid.String())))
	return hex.EncodeToString(m.Sum(nil)[:8])
}
