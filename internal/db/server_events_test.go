package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// With events off (or no database, or the demo user) nothing is attempted;
// an unknown event name is dropped before any query.
func TestServerEventsAreSkipped(t *testing.T) {
	ctx := context.Background()
	t.Setenv("RIVOLT_EVENTS", "")
	RecordServerEvent(ctx, nil, uuid.New(), EventSignupComplete, nil) // nil db: no panic
	if ServerEventsEnabled() {
		t.Error("enabled with RIVOLT_EVENTS unset")
	}
	t.Setenv("RIVOLT_EVENTS", "true")
	if ServerEventsEnabled() {
		t.Error("enabled by RIVOLT_EVENTS alone; server events need their own switch")
	}
	t.Setenv("RIVOLT_SERVER_EVENTS", "true")
	if !ServerEventsEnabled() {
		t.Error("not enabled with both switches on")
	}
	t.Setenv("RIVOLT_EVENTS", "")
	if ServerEventsEnabled() {
		t.Error("enabled with product events off")
	}
	if !skipServerEvent(nil, uuid.New()) {
		t.Error("nil db not skipped")
	}
}

func TestSkipServerEventForDemoAndNilUsers(t *testing.T) {
	t.Setenv("RIVOLT_EVENTS", "true")
	t.Setenv("RIVOLT_SERVER_EVENTS", "true")
	d := newFakeDB(t)
	if !skipServerEvent(d, UserIDFor(DemoUsername)) || !skipServerEvent(d, uuid.Nil) {
		t.Error("demo or nil user not skipped")
	}
	if skipServerEvent(d, uuid.New()) {
		t.Error("a real user was skipped")
	}
}

func TestSignupMethodRoundTrip(t *testing.T) {
	if got := signupMethod(WithSignupMethod(context.Background(), "oidc")); got != "oidc" {
		t.Errorf("got %q", got)
	}
	if signupMethod(context.Background()) != "" {
		t.Error("method set on a bare context")
	}
}

func TestDoNotTrackContext(t *testing.T) {
	if DoNotTrack(context.Background()) || !DoNotTrack(WithDoNotTrack(context.Background())) {
		t.Error("DoNotTrack does not round-trip")
	}
}
