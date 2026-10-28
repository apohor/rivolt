//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/onboardmail"
	"github.com/apohor/rivolt/internal/rivianexpiry"
	"github.com/apohor/rivolt/internal/statement"
)

// The one-click link in the expiry reminder stops those reminders and nothing else; GET never
// does it; a link signed for another email (the welcome and setup emails, the statement) does
// not work here.
func TestRivianExpiryReminderOneClickStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "expiry-stop")
	if err != nil {
		t.Fatal(err)
	}
	signer := statement.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	now := time.Now()
	a := &rivianExpiryEmailAPI{db: pool, signer: signer, now: func() time.Time { return now }}
	tok := func(purpose string) string { return signer.Sign(uid, purpose, now.Add(time.Hour)) }
	req := func(method, token string) *http.Request {
		return httptest.NewRequest(method, "/api/rivian-expiry-email/unsubscribe?t="+token, nil)
	}
	off := func() bool {
		o, err := db.RivianExpiryEmailsOff(ctx, pool, uid)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}

	// GET only shows the confirmation.
	w := httptest.NewRecorder()
	a.unsubscribeGet(w, req("GET", tok(rivianexpiry.UnsubPurpose)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Stop these reminders") || off() {
		t.Fatalf("GET: %d, off=%v: it must show a form and change nothing", w.Code, off())
	}
	// Links signed for other emails do not work.
	for _, purpose := range []string{onboardmail.UnsubPurpose, "2026-10", "anything"} {
		w = httptest.NewRecorder()
		a.unsubscribePost(w, req("POST", tok(purpose)))
		if w.Code != http.StatusBadRequest || off() {
			t.Fatalf("a link signed for %q: %d, off=%v, want 400 and no change", purpose, w.Code, off())
		}
	}
	// An expired or forged link does not work.
	w = httptest.NewRecorder()
	a.unsubscribePost(w, req("POST", signer.Sign(uid, rivianexpiry.UnsubPurpose, now.Add(-time.Hour))))
	if w.Code != http.StatusBadRequest || off() {
		t.Fatalf("an expired link: %d off=%v", w.Code, off())
	}
	w = httptest.NewRecorder()
	a.unsubscribePost(w, req("POST", tok(rivianexpiry.UnsubPurpose)+"x"))
	if w.Code != http.StatusBadRequest || off() {
		t.Fatalf("a forged link: %d off=%v", w.Code, off())
	}
	// The right one stops the reminders, twice is the same, and only these.
	for i := 0; i < 2; i++ {
		w = httptest.NewRecorder()
		a.unsubscribePost(w, req("POST", tok(rivianexpiry.UnsubPurpose)))
		if w.Code != 200 || !off() {
			t.Fatalf("POST %d: %d off=%v", i, w.Code, off())
		}
	}
	if other, _ := db.EngagementEmailsOff(ctx, pool, uid); other {
		t.Fatal("stopping the expiry reminders switched off the welcome and setup emails")
	}

	// Settings turns them back on, and off again.
	put := func(body string) int {
		r := httptest.NewRequest("PUT", "/api/settings/rivian-expiry-email", strings.NewReader(body)).WithContext(auth.WithUser(ctx, uid))
		rec := httptest.NewRecorder()
		a.putSettings(uid, rec, r)
		return rec.Code
	}
	if c := put(`{"enabled": true}`); c != 200 || off() {
		t.Fatalf("turn on: %d off=%v", c, off())
	}
	if c := put(`{"enabled": false}`); c != 200 || !off() {
		t.Fatalf("turn off: %d off=%v", c, off())
	}
	if c := put(`{}`); c != http.StatusBadRequest {
		t.Fatalf("empty body: %d, want 400", c)
	}
}

// With the sender on the Settings toggle answers 200 and round-trips; with it off the signed
// unsubscribe links in mail already sent still work.
func TestExpiryReminderRoutesThroughTheRouter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "expiry-routes")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	signer := statement.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	call := func(sender bool, method, path, body string) *httptest.ResponseRecorder {
		router := New(Deps{Auth: svc, AuthEnforced: true, DB: pool, SystemDB: pool, StatementSigner: signer, RivianExpiryEmails: sender})
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, strings.NewReader(body))
		req.RemoteAddr = "203.0.113.9:4000"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req.WithContext(auth.WithUser(req.Context(), uid)))
		return rec
	}

	if rec := call(true, "GET", "/api/settings/rivian-expiry-email", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("sender on, GET: %d %q, want 200 enabled", rec.Code, rec.Body.String())
	}
	if rec := call(true, "PUT", "/api/settings/rivian-expiry-email", `{"enabled": false}`); rec.Code != 200 {
		t.Fatalf("sender on, PUT: %d %q", rec.Code, rec.Body.String())
	}
	if rec := call(true, "GET", "/api/settings/rivian-expiry-email", ""); !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("the choice did not stick: %q", rec.Body.String())
	}
	// Sender off: the settings route is gone, the stored choice is not.
	if rec := call(false, "GET", "/api/settings/rivian-expiry-email", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("sender off, GET: %d, want 404", rec.Code)
	}
	// The link in an email already sent still works after the switch is turned off.
	tok := signer.Sign(uid, rivianexpiry.UnsubPurpose, time.Now().Add(time.Hour))
	if err := db.SetRivianExpiryEmailsOff(ctx, pool, uid, false); err != nil {
		t.Fatal(err)
	}
	if rec := call(false, "GET", "/api/rivian-expiry-email/unsubscribe?t="+tok, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Stop these reminders") {
		t.Fatalf("sender off, unsubscribe GET: %d %q, want the confirmation page", rec.Code, rec.Body.String())
	}
	if rec := call(false, "POST", "/api/rivian-expiry-email/unsubscribe?t="+tok, ""); rec.Code != 200 {
		t.Fatalf("sender off, unsubscribe POST: %d %q", rec.Code, rec.Body.String())
	}
	if off, _ := db.RivianExpiryEmailsOff(ctx, pool, uid); !off {
		t.Fatal("the unsubscribe link did not stop the reminders with the sender off")
	}
}
