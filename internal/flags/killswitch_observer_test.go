package flags

import "testing"

type kcall struct {
	hadOld    bool
	oldPaused bool
	curPaused bool
	actor     string
}

func observe(s *Store) *[]kcall {
	var calls []kcall
	s.SetKillSwitchObserver(func(old *KillSwitchState, cur KillSwitchState) {
		c := kcall{curPaused: cur.Paused, actor: cur.Actor}
		if old != nil {
			c.hadOld, c.oldPaused = true, old.Paused
		}
		calls = append(calls, c)
	})
	return &calls
}

// The observer fires on every Paused change (a local SetKillSwitch or
// a refresh picking up another pod's flip), never on a no-change write,
// and once at boot when the switch is already on.
func TestKillSwitchObserver(t *testing.T) {
	s := &Store{}
	calls := observe(s)

	s.storeKillSwitch(KillSwitchState{Paused: false}) // first load, off: nothing to say
	if len(*calls) != 0 {
		t.Fatalf("first load off fired: %+v", *calls)
	}
	s.storeKillSwitch(KillSwitchState{Paused: true, Actor: "ops", Reason: "rivian asked"})
	s.storeKillSwitch(KillSwitchState{Paused: true, Actor: "ops", Reason: "still"}) // reason edit only
	s.storeKillSwitch(KillSwitchState{Paused: false, Actor: "ops"})
	got := *calls
	if len(got) != 2 {
		t.Fatalf("calls = %+v, want 2 (on, off)", got)
	}
	if !got[0].curPaused || got[0].oldPaused || !got[0].hadOld || got[0].actor != "ops" {
		t.Fatalf("on call = %+v", got[0])
	}
	if got[1].curPaused || !got[1].oldPaused {
		t.Fatalf("off call = %+v", got[1])
	}
}

func TestKillSwitchObserver_AlreadyPausedAtBootFiresOnce(t *testing.T) {
	s := &Store{}
	calls := observe(s)
	s.storeKillSwitch(KillSwitchState{Paused: true, Actor: "ops"})
	s.storeKillSwitch(KillSwitchState{Paused: true, Actor: "ops"})
	if len(*calls) != 1 || (*calls)[0].hadOld {
		t.Fatalf("calls = %+v, want one call with no old state", *calls)
	}
}

func TestKillSwitch_NoObserverAndSnapshotStillStored(t *testing.T) {
	s := &Store{}
	s.storeKillSwitch(KillSwitchState{Paused: true})
	if !s.KillSwitch().Paused {
		t.Fatal("snapshot not stored without an observer")
	}
}
