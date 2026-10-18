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

// Every way a server event can be dropped reports its own outcome, so a
// missing row can be explained from the metric.
func TestServerEventOutcomes(t *testing.T) {
	var got []string
	ServerEventObserver = func(event, outcome string) { got = append(got, event+":"+outcome) }
	defer func() { ServerEventObserver = nil }()
	ctx := context.Background()
	user := uuid.New()
	d := newFakeDB(t)

	t.Setenv("RIVOLT_EVENTS", "")
	t.Setenv("RIVOLT_SERVER_EVENTS", "")
	RecordServerEvent(ctx, d, user, EventSignupComplete, nil)

	t.Setenv("RIVOLT_EVENTS", "true")
	t.Setenv("RIVOLT_SERVER_EVENTS", "true")
	RecordServerEvent(WithDoNotTrack(ctx), d, user, EventSignupComplete, nil)
	RecordServerEvent(ctx, d, UserIDFor(DemoUsername), EventRivianConnectOK, nil)
	RecordServerEvent(ctx, d, uuid.Nil, EventRivianConnectOK, nil)
	RecordServerEvent(ctx, d, user, "not_an_event", nil) // unknown names report nothing

	want := []string{
		"signup_complete:skipped_disabled",
		"signup_complete:skipped_dnt",
		"rivian_connect_ok:skipped_other",
		"rivian_connect_ok:skipped_other",
	}
	if len(got) != len(want) {
		t.Fatalf("outcomes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("outcome %d = %q, want %q", i, got[i], want[i])
		}
	}
}
