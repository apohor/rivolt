package db

import (
	"context"
	"errors"
	"testing"
)

func TestPinKeepsConnectionsForBoot(t *testing.T) {
	d := newFakeDB(t)
	d.SetMaxOpenConns(6)
	g := pinGate(d)
	if cap(g) != 6-pinReserve {
		t.Fatalf("gate size %d, want %d", cap(g), 6-pinReserve)
	}
	for range cap(g) {
		g <- struct{}{}
	}
	if _, _, err := Pin(context.Background(), d); !errors.Is(err, ErrPinGateFull) {
		t.Fatalf("with every slot taken got %v, want ErrPinGateFull", err)
	}
}

func TestPinFreesTheSlotWhenTheConnectionFails(t *testing.T) {
	d := newFakeDB(t) // nothing listens: Conn fails
	d.SetMaxOpenConns(3)
	for i := range 3 {
		if _, _, err := Pin(context.Background(), d); err == nil || errors.Is(err, ErrPinGateFull) {
			t.Fatalf("try %d: got %v, want a connect error that frees its slot", i, err)
		}
	}
}

func TestPinSmallPoolStillAllowsOne(t *testing.T) {
	d := newFakeDB(t)
	d.SetMaxOpenConns(1)
	if got := cap(pinGate(d)); got != 1 {
		t.Fatalf("gate size %d, want 1", got)
	}
}

func TestPinUnlimitedPoolHasNoGate(t *testing.T) {
	if pinGate(newFakeDB(t)) != nil {
		t.Fatal("an unlimited pool must not be gated")
	}
}
