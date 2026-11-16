package entitlements

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func redatePlan() RedatePlan {
	return RedatePlan{
		Move:  []uuid.UUID{uuid.MustParse("00000000-0000-0000-0000-000000000001"), uuid.MustParse("00000000-0000-0000-0000-000000000002")},
		Grant: []uuid.UUID{uuid.MustParse("00000000-0000-0000-0000-000000000003")},
		From:  time.Date(2027, 11, 16, 22, 0, 0, 0, time.UTC),
		To:    time.Date(2027, 11, 17, 0, 0, 0, 0, time.UTC),
	}
}

func TestRedateRefusal(t *testing.T) {
	p := redatePlan()
	ok := RedateExpect{MoveCount: 2, MoveSHA: p.MoveHash(), GrantCount: 1, GrantSHA: p.GrantHash()}
	if got := redateRefusal(p, ok); got != "" {
		t.Fatalf("the dry run's own values refused: %q", got)
	}
	cases := map[string]RedateExpect{
		"needs":            {MoveCount: -1, GrantCount: -1},
		"not the dry run":  {MoveCount: 2, MoveSHA: p.MoveHash(), GrantCount: 0, GrantSHA: p.GrantHash()},
		"not the dry run ": {MoveCount: 2, MoveSHA: "x", GrantCount: 1, GrantSHA: p.GrantHash()},
	}
	for want, e := range cases {
		got := redateRefusal(p, e)
		if !strings.Contains(got, strings.TrimSpace(want)) || !strings.Contains(got, "Nothing was written") {
			t.Errorf("%q: got %q", want, got)
		}
	}
	if got := redateRefusal(RedatePlan{}, ok); !strings.Contains(got, "nothing to do") {
		t.Errorf("empty plan: %q", got)
	}
}

func TestRedateUndoSQLRestoresTheOldEnd(t *testing.T) {
	got := redatePlan().UndoSQL()
	for _, want := range []string{
		"BEGIN;", "COMMIT;",
		"UPDATE entitlements SET current_period_end = '2027-11-16T22:00:00Z' WHERE source = 'founding' AND current_period_end = '2027-11-17T00:00:00Z'",
		"'00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002'",
		"DELETE FROM entitlements WHERE source = 'founding' AND current_period_end = '2027-11-17T00:00:00Z' AND user_id IN ('00000000-0000-0000-0000-000000000003')",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("undo lacks %q:\n%s", want, got)
		}
	}
}
