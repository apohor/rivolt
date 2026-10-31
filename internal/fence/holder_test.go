package fence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type holderSrc struct {
	st  State
	err error
	at  time.Time
}

func (h holderSrc) Fence(string) (Token, State)       { return Token{Group: "g", Pod: "p", Epoch: 4}, h.st }
func (h holderSrc) Deadline(string) (time.Time, bool) { return h.at, !h.at.IsZero() }
func (h holderSrc) FenceUser(context.Context, string) (Token, State, error) {
	return Token{Group: "g", Pod: "p", Epoch: 4}, h.st, h.err
}

// plainSrc has only Fence: neither users nor deadlines.
type plainSrc struct{ st State }

func (p plainSrc) Fence(string) (Token, State) { return Token{}, p.st }

func TestHolderBeforeTheStoreIsInstalled(t *testing.T) {
	ctx := context.Background()
	var h Holder
	if _, st := h.Fence("v"); st != Off {
		t.Errorf("not required, not installed: Fence %v, want Off", st)
	}
	if _, st, err := h.FenceUser(ctx, "u"); st != Off || err != nil {
		t.Errorf("not required, not installed: FenceUser %v %v, want Off", st, err)
	}
	h.Require(true)
	if _, st := h.Fence("v"); st != Lost {
		t.Errorf("lead mode, not installed: Fence %v, want Lost (fail closed)", st)
	}
	if _, st, err := h.FenceUser(ctx, "u"); st != Lost || err != nil {
		t.Errorf("lead mode, not installed: FenceUser %v %v, want Lost", st, err)
	}
	if _, ok := h.Deadline("v"); ok {
		t.Error("a deadline from no source")
	}
}

func TestHolderReachesConsumersStartedBeforeTheInstall(t *testing.T) {
	ctx := context.Background()
	var h Holder
	h.Require(true)
	// A consumer keeps h itself, as the syncers do, and asks on every use.
	var src Source = &h
	at := time.Now().Add(10 * time.Second)
	h.Set(holderSrc{st: Held, at: at})
	if tok, st := src.Fence("v"); st != Held || tok.Epoch != 4 {
		t.Errorf("after install: %v %+v, want Held with the lease's token", st, tok)
	}
	if tok, st, err := h.FenceUser(ctx, "u"); st != Held || tok.Epoch != 4 || err != nil {
		t.Errorf("FenceUser after install: %v %+v %v", st, tok, err)
	}
	if got, ok := h.Deadline("v"); !ok || !got.Equal(at) {
		t.Errorf("deadline = %v %v, want %v", got, ok, at)
	}
	h.Set(holderSrc{st: Lost, err: errors.New("db")})
	if _, st := h.Fence("v"); st != Lost {
		t.Errorf("a later source replaces the first: %v", st)
	}
	if _, _, err := h.FenceUser(ctx, "u"); err == nil {
		t.Error("the source's error is lost")
	}
	// A source that cannot resolve users is missing for FenceUser, not Off, in lead mode.
	h.Set(plainSrc{Held})
	if _, st, _ := h.FenceUser(ctx, "u"); st != Lost {
		t.Errorf("FenceUser on a source without it: %v, want Lost in lead mode", st)
	}
	if _, ok := h.Deadline("v"); ok {
		t.Error("a deadline from a source without deadlines")
	}
	h.Require(false)
	if _, st, _ := h.FenceUser(ctx, "u"); st != Off {
		t.Errorf("lead mode off: %v, want Off", st)
	}
}

func TestHolderIsSafeUnderConcurrentUse(t *testing.T) {
	var h Holder
	h.Require(true)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				h.Fence("v")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				h.Set(holderSrc{st: Held})
			}
		}()
	}
	wg.Wait()
}
