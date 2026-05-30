package secrets

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// TestLoadRivianSession_NilStore: a nil *Store is the legitimate
// "secrets disabled" wiring — main builds a Store only when a KEK
// is configured. Load should answer with a zero Session and a nil
// error so callers can fall back to "logged out" without special
// casing.
func TestLoadRivianSession_NilStore(t *testing.T) {
	got, err := LoadRivianSession(context.Background(), nil, uuid.New())
	if err != nil {
		t.Fatalf("LoadRivianSession(nil store): err = %v, want nil", err)
	}
	if (got != rivian.Session{}) {
		t.Fatalf("LoadRivianSession(nil store): got %+v, want zero", got)
	}
}

// TestSaveRivianSession_NilStore: same wiring concern as the load
// test — a nil store is a valid no-op, not an error, otherwise
// the LoginHandler would have to branch on every call.
func TestSaveRivianSession_NilStore(t *testing.T) {
	sess := rivian.Session{
		Email:            "test@example.com",
		UserSessionToken: "ust-token",
	}
	if err := SaveRivianSession(context.Background(), nil, uuid.New(), sess); err != nil {
		t.Fatalf("SaveRivianSession(nil store): err = %v, want nil", err)
	}
}

// TestRivianSessionJSONRoundTrip: the on-disk format is
// `json.Marshal(rivian.Session)`. If anyone ever quietly drops a
// json tag, sealed rows would silently lose data on the next write.
// This test pins every field through a marshal → unmarshal cycle so
// future struct changes either keep the wire shape or fail loudly.
func TestRivianSessionJSONRoundTrip(t *testing.T) {
	want := rivian.Session{
		Email:            "driver@example.com",
		CSRFToken:        "csrf-abc",
		AppSessionToken:  "app-def",
		UserSessionToken: "user-ghi",
		AccessToken:      "access-jkl",
		RefreshToken:     "refresh-mno",
		// AuthenticatedAt: trimmed to seconds because Postgres
		// timestamptz round-trips at microsecond precision but
		// JSON marshal already serializes the wall clock; we
		// compare via Equal to dodge monotonic-clock readings
		// that survive json round-tripping as plain UTC.
		AuthenticatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	buf, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got rivian.Session
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Email != want.Email ||
		got.CSRFToken != want.CSRFToken ||
		got.AppSessionToken != want.AppSessionToken ||
		got.UserSessionToken != want.UserSessionToken ||
		got.AccessToken != want.AccessToken ||
		got.RefreshToken != want.RefreshToken ||
		!got.AuthenticatedAt.Equal(want.AuthenticatedAt) {
		t.Fatalf("round-trip drift:\n got  %+v\n want %+v", got, want)
	}
}

// TestRivianSessionConstantStable pins the storage name so a
// rename can't go in unnoticed — the name is part of the
// at-rest contract: a renamed constant would orphan every
// existing user_secrets row on next deploy.
func TestRivianSessionConstantStable(t *testing.T) {
	if rivianSessionName != "rivian.session" {
		t.Fatalf("rivianSessionName = %q, want %q (stored rows depend on this)",
			rivianSessionName, "rivian.session")
	}
}

// Nil-store no-op: same wiring concern as the session helpers — a
// secrets-disabled deployment must not error on the MFA handoff path.
func TestPendingMFA_NilStore(t *testing.T) {
	ctx := context.Background()
	uid := uuid.New()
	if err := SavePendingMFA(ctx, nil, uid, rivian.PendingMFA{OTPToken: "x"}); err != nil {
		t.Fatalf("SavePendingMFA(nil store): err = %v, want nil", err)
	}
	if _, ok := LoadPendingMFA(ctx, nil, uid); ok {
		t.Fatalf("LoadPendingMFA(nil store): ok = true, want false")
	}
	if err := ClearPendingMFA(ctx, nil, uid); err != nil {
		t.Fatalf("ClearPendingMFA(nil store): err = %v, want nil", err)
	}
}

// TestPendingMFAJSONRoundTrip pins the at-rest wire shape of the
// shared OTP challenge so a dropped json tag can't silently lose a
// field that the resuming pod needs to finish the second leg.
func TestPendingMFAJSONRoundTrip(t *testing.T) {
	want := rivian.PendingMFA{
		Email:           "driver@example.com",
		OTPToken:        "otp-abc",
		CSRFToken:       "csrf-def",
		AppSessionToken: "app-ghi",
		IssuedAt:        time.Unix(1_700_000_000, 0).UTC(),
	}
	buf, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got rivian.PendingMFA
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Email != want.Email ||
		got.OTPToken != want.OTPToken ||
		got.CSRFToken != want.CSRFToken ||
		got.AppSessionToken != want.AppSessionToken ||
		!got.IssuedAt.Equal(want.IssuedAt) {
		t.Fatalf("round-trip drift:\n got  %+v\n want %+v", got, want)
	}
}

// TestRivianPendingMFAConstantStable pins the storage name: an
// in-flight challenge written by one pod is read by another, so a
// rename would silently break every cross-pod handoff in progress.
func TestRivianPendingMFAConstantStable(t *testing.T) {
	if rivianPendingMFAName != "rivian.pending-mfa" {
		t.Fatalf("rivianPendingMFAName = %q, want %q (cross-pod rows depend on this)",
			rivianPendingMFAName, "rivian.pending-mfa")
	}
}
