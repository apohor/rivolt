//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// Terms acceptance in Postgres: never accepted at first, accept records the
// version and time, a repeat keeps the original time, a stale version is
// refused, a new version replaces it, and the export carries it.
func TestTermsAcceptEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "terms-user")
	if err != nil {
		t.Fatal(err)
	}
	if v := termsView(ctx, pool, uid); v["accepted"] != nil || v["accepted_at"] != nil || v["current"] != currentTermsVersion {
		t.Fatalf("before accepting: %v", v)
	}
	accept := func(version string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		handleTermsAccept(pool)(uid, rec, httptest.NewRequest("POST", "/api/terms/accept", strings.NewReader(`{"version":"`+version+`"}`)))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	code, _ := accept("1999-01-01")
	if code != 409 {
		t.Fatalf("stale version = %d, want 409", code)
	}
	if v := termsView(ctx, pool, uid); v["accepted"] != nil {
		t.Fatalf("a refused accept was recorded: %v", v)
	}
	code, body := accept(currentTermsVersion)
	first := body["terms"].(map[string]any)
	if code != 200 || first["accepted"] != currentTermsVersion || first["accepted_at"] == nil {
		t.Fatalf("accept = %d %v", code, body)
	}
	time.Sleep(1100 * time.Millisecond)
	code, body = accept(currentTermsVersion)
	again := body["terms"].(map[string]any)
	if code != 200 || again["accepted_at"] != first["accepted_at"] {
		t.Errorf("repeat accept changed the time: %v -> %v", first["accepted_at"], again["accepted_at"])
	}
	// A different (later) version replaces the record with a new time.
	if err := db.AcceptTerms(ctx, pool, uid, "2027-01-01"); err != nil {
		t.Fatal(err)
	}
	if v := termsView(ctx, pool, uid); v["accepted"] != "2027-01-01" || v["accepted_at"] == first["accepted_at"] {
		t.Errorf("new version not recorded: %v", v)
	}
	terms, err := db.TermsFor(ctx, pool, uid)
	if err != nil {
		t.Fatal(err)
	}
	meta := exportData{Terms: terms}.meta()
	if meta["terms_accepted_version"] != "2027-01-01" || meta["terms_accepted_at"] == nil {
		t.Errorf("export meta = %v", meta)
	}
}
