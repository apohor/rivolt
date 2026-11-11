package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/fence"
)

func allOn() map[string]string {
	m := map[string]string{"RIVOLT_NO_STANDING_TOKENS": "1"}
	for _, k := range noStandingTokensRequires {
		m[k] = "1"
	}
	return m
}

func TestNoStandingTokensOffNeedsNothing(t *testing.T) {
	if err := noStandingTokensError(envOf(nil)); err != nil {
		t.Fatalf("flag off: %v", err)
	}
	if err := noStandingTokensError(envOf(map[string]string{"RIVOLT_NO_STANDING_TOKENS": "0"})); err != nil {
		t.Fatalf("flag 0: %v", err)
	}
}

func TestNoStandingTokensAllOnIsFine(t *testing.T) {
	if err := noStandingTokensError(envOf(allOn())); err != nil {
		t.Fatalf("all on: %v", err)
	}
}

// One exit-1 case per missing setting, and permit enforcement is not among them.
func TestNoStandingTokensRefusesEachMissingSetting(t *testing.T) {
	want := []string{"RIVOLT_SINGLE_OWNER", "RIVOLT_GROUP_LEAD", "RIVOLT_FORWARD", "RIVOLT_OWNER_DISCOVERY", "RIVOLT_ACQUIRE_ON_REQUEST"}
	if len(noStandingTokensRequires) != len(want) {
		t.Fatalf("requires = %v, want %v", noStandingTokensRequires, want)
	}
	for _, k := range want {
		for _, v := range []string{"", "0"} {
			m := allOn()
			m[k] = v
			err := noStandingTokensError(envOf(m))
			if err == nil || !strings.Contains(err.Error(), k) {
				t.Errorf("%s=%q: err = %v, want a refusal naming it", k, v, err)
			}
		}
	}
	m := allOn()
	delete(m, "RIVOLT_PERMIT_ENFORCE")
	if err := noStandingTokensError(envOf(m)); err != nil {
		t.Fatalf("permit enforce must not be required: %v", err)
	}
}

func TestGroupHold(t *testing.T) {
	cases := []struct {
		name        string
		st          fence.State
		err         error
		held, known bool
	}{
		{"held", fence.Held, nil, true, true},
		{"warming", fence.Warming, nil, true, true},
		{"lead mode off", fence.Off, nil, true, true},
		{"lost", fence.Lost, nil, false, true},
		{"unreadable", fence.Held, errors.New("db"), false, false},
	}
	for _, c := range cases {
		held, known := groupHold(fakeUserFencer{st: c.st, err: c.err})(context.Background(), uuid.New())
		if held != c.held || known != c.known {
			t.Errorf("%s: held=%v known=%v, want %v %v", c.name, held, known, c.held, c.known)
		}
	}
}
