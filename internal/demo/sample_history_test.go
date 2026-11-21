package demo

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// The older history is written only where the live Rivian client is forbidden and the sim controls are on (the preview admin site):
// prod's environment, either flag alone, or none refuses before any database is touched, and the public demo account is refused.
func TestSampleHistoryRefusesWithoutTheGuard(t *testing.T) {
	uid := uuid.New()
	for name, m := range map[string]map[string]string{
		"prod: neither flag":  {},
		"sim controls only":   {"RIVOLT_SIM_CONTROLS": "true"},
		"live forbidden only": {"RIVOLT_RIVIAN_LIVE_FORBIDDEN": "true"},
		"sim controls off":    {"RIVOLT_SIM_CONTROLS": "false", "RIVOLT_RIVIAN_LIVE_FORBIDDEN": "true"},
	} {
		w := &SampleHistoryWriter{Getenv: env(m)} // no DB: a refusal must come first
		if w.Allowed() {
			t.Errorf("%s: allowed", name)
		}
		if _, err := w.Run(context.Background(), uid); !errors.Is(err, ErrSampleHistoryRefused) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	var none *SampleHistoryWriter
	if none.Allowed() {
		t.Error("a nil writer is allowed")
	}
	ok := &SampleHistoryWriter{Getenv: env(map[string]string{"RIVOLT_SIM_CONTROLS": "true", "RIVOLT_RIVIAN_LIVE_FORBIDDEN": "true"})}
	if !ok.Allowed() {
		t.Fatal("the preview admin environment is refused")
	}
	if _, err := ok.Run(context.Background(), UserID()); !errors.Is(err, ErrSampleHistoryDemo) {
		t.Errorf("the demo account: err = %v, want ErrSampleHistoryDemo", err)
	}
	if _, err := ok.Run(context.Background(), uuid.Nil); !errors.Is(err, ErrSampleHistoryDemo) {
		t.Errorf("no account: err = %v", err)
	}
}
