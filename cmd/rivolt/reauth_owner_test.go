package main

import (
	"context"
	"errors"
	"testing"

	"github.com/apohor/rivolt/internal/fence"
)

func TestReauthCarries(t *testing.T) {
	bg := context.Background()
	for want, ctx := range map[string]context.Context{
		"token":   fence.With(bg, fence.Token{Group: "g"}),
		"none":    bg,
	} {
		if got := reauthCarries(ctx); got != want {
			t.Errorf("reauthCarries = %q, want %q", got, want)
		}
	}
}

type fakeUserFencer struct {
	tok fence.Token
	st  fence.State
	err error
}

func (f fakeUserFencer) FenceUser(context.Context, string) (fence.Token, fence.State, error) {
	return f.tok, f.st, f.err
}

// A raise carries the group's token only when this pod holds the group (Held, or Warming); lead mode off, a group held elsewhere and an unreadable
// answer leave the context as it was, so the raise is unguarded or, owner-only, refused.
func TestReauthOwnerCtx(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 4}
	for name, tc := range map[string]struct {
		f    userFencer
		want bool
	}{
		"nil source":     {nil, false},
		"lead off":       {fakeUserFencer{st: fence.Off}, false},
		"held":           {fakeUserFencer{tok: tok, st: fence.Held}, true},
		"warming":        {fakeUserFencer{tok: tok, st: fence.Warming}, true},
		"held elsewhere": {fakeUserFencer{st: fence.Lost}, false},
		"cannot be told": {fakeUserFencer{tok: tok, st: fence.Held, err: errors.New("db down")}, false},
	} {
		got, has := fence.From(reauthOwnerCtx(context.Background(), tc.f, "u"))
		if has != tc.want || (has && got != tok) {
			t.Errorf("%s: token %+v (has %v), want has=%v", name, got, has, tc.want)
		}
	}
	if reauthRefusedReason(fence.ErrFenced) != "fenced" || reauthRefusedReason(errors.New("x")) != "not_owner" {
		t.Error("refusal reasons")
	}
}
