//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/onboardmail"
	"github.com/apohor/rivolt/internal/statement"
)

// The one-click unsubscribe and the Settings toggle, against Postgres: a
// signed onboarding link turns the emails off (GET only shows a page), the
// statement email's tokens are refused here and these tokens are refused by
// the statement unsubscribe, and the toggle round-trips.
func TestOnboardingEmailUnsubscribeAndSettings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "oe-user")
	if err != nil {
		t.Fatal(err)
	}
	signer := statement.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	a := &onboardingEmailAPI{db: pool, signer: signer, now: time.Now}

	off := func() bool { o, _ := db.EngagementEmailsOff(ctx, pool, uid); return o }
	do := func(h http.HandlerFunc, method, tok string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(method, "/api/onboarding-email/unsubscribe?t="+tok, nil))
		return rec
	}
	good := signer.Sign(uid, onboardmail.UnsubPurpose, time.Now().Add(time.Hour))

	if rec := do(a.unsubscribeGet, "GET", good); rec.Code != 200 || off() || !strings.Contains(rec.Body.String(), "<form method=\"post\"") {
		t.Fatalf("GET: %d off=%v", rec.Code, off())
	}
	// A statement-email token (period YYYY-MM) is not valid here.
	if rec := do(a.unsubscribePost, "POST", signer.Sign(uid, "2026-09", time.Now().Add(time.Hour))); rec.Code != 400 || off() {
		t.Fatalf("statement token accepted: %d off=%v", rec.Code, off())
	}
	if rec := do(a.unsubscribePost, "POST", "garbage"); rec.Code != 400 {
		t.Fatalf("garbage token: %d", rec.Code)
	}
	if rec := do(a.unsubscribePost, "POST", signer.Sign(uid, onboardmail.UnsubPurpose, time.Now().Add(-time.Hour))); rec.Code != 400 || off() {
		t.Fatalf("expired token: %d", rec.Code)
	}
	if rec := do(a.unsubscribePost, "POST", good); rec.Code != 200 || !off() {
		t.Fatalf("POST: %d off=%v", rec.Code, off())
	}
	if rec := do(a.unsubscribePost, "POST", good); rec.Code != 200 || !off() { // idempotent
		t.Fatalf("repeat POST: %d", rec.Code)
	}

	// The statement unsubscribe refuses our token.
	stm := &statementAPI{signer: signer, now: time.Now}
	rec := httptest.NewRecorder()
	stm.unsubscribePost(rec, httptest.NewRequest("POST", "/api/statement-email/unsubscribe?t="+good, nil))
	if rec.Code != 400 {
		t.Errorf("statement unsubscribe accepted an onboarding token: %d", rec.Code)
	}

	// Settings toggle.
	get := func() bool {
		rec := httptest.NewRecorder()
		a.getSettings(uid, rec, httptest.NewRequest("GET", "/api/settings/onboarding-email", nil))
		var b map[string]bool
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return b["enabled"]
	}
	if get() {
		t.Error("enabled after unsubscribe")
	}
	put := func(body string) int {
		rec := httptest.NewRecorder()
		a.putSettings(uid, rec, httptest.NewRequest("PUT", "/api/settings/onboarding-email", strings.NewReader(body)))
		return rec.Code
	}
	if code := put(`{"enabled":true}`); code != 200 || !get() || off() {
		t.Errorf("turn on: %d get=%v off=%v", code, get(), off())
	}
	if code := put(`{}`); code != 400 {
		t.Errorf("missing field: %d", code)
	}
	if code := put(`{"enabled":false}`); code != 200 || get() {
		t.Errorf("turn off: %d", code)
	}
}
