package main

import (
	"context"
	"errors"
	"testing"

	"github.com/apohor/rivolt/internal/fence"
)

type fakeUngroupedFencer struct {
	fakeUserFencer
	none bool
	err  error
}

func (f fakeUngroupedFencer) UserUngrouped(context.Context, string) (bool, error) {
	return f.none, f.err
}

// A user with no group has no owner, ever: their raise is marked Unowned so owner-only raises let it through. A group another pod holds, or an
// answer that cannot be had, is not that: those stay refused.
func TestReauthOwnerCtxUngroupedUser(t *testing.T) {
	for name, tc := range map[string]struct {
		f    userFencer
		want bool
	}{
		"no group":             {fakeUngroupedFencer{fakeUserFencer{st: fence.Lost}, true, nil}, true},
		"group held elsewhere": {fakeUngroupedFencer{fakeUserFencer{st: fence.Lost}, false, nil}, false},
		"cannot be told":       {fakeUngroupedFencer{fakeUserFencer{st: fence.Lost}, true, errors.New("db down")}, false},
		"we hold it":           {fakeUngroupedFencer{fakeUserFencer{tok: fence.Token{Group: "g"}, st: fence.Held}, true, nil}, false},
	} {
		if got := fence.IsUnowned(reauthOwnerCtx(context.Background(), tc.f, "u")); got != tc.want {
			t.Errorf("%s: unowned %v, want %v", name, got, tc.want)
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
