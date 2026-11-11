package main

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/rivian"
)

// perUserFencer answers a state per user, an error for the ones in errs.
type perUserFencer struct {
	st   map[string]fence.State
	errs map[string]bool
}

func (f perUserFencer) FenceUser(_ context.Context, uid string) (fence.Token, fence.State, error) {
	if f.errs[uid] {
		return fence.Token{}, fence.Lost, errors.New("db down")
	}
	return fence.Token{}, f.st[uid], nil
}

// The gauge counts signed-in clients of users whose group this pod does not hold: Held and Warming and lead-mode-off are held, Lost and an
// answer that cannot be had are not, and a client with no session token counts for nothing.
func TestTokensLoadedUnheld(t *testing.T) {
	ids := map[string]uuid.UUID{}
	for _, n := range []string{"held", "warming", "off", "lost", "unreadable", "nosession"} {
		ids[n] = uuid.New()
	}
	reg := rivian.NewLiveAccountRegistry(func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive() })
	for n, uid := range ids {
		if n == "nosession" {
			reg.For(uid)
			continue
		}
		reg.For(uid).Restore(rivian.Session{UserSessionToken: "tok-" + n})
	}
	f := perUserFencer{
		st: map[string]fence.State{
			ids["held"].String(): fence.Held, ids["warming"].String(): fence.Warming, ids["off"].String(): fence.Off,
			ids["lost"].String(): fence.Lost, ids["nosession"].String(): fence.Lost,
		},
		errs: map[string]bool{ids["unreadable"].String(): true},
	}
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "t"})
	tokensLoadedUnheld(context.Background(), reg, f, g)
	if got := testutil.ToFloat64(g); got != 2 {
		t.Fatalf("tokens loaded for unheld groups = %v, want 2 (lost and unreadable)", got)
	}
}

// A registry that is not the live one has no tokens: the gauge stays 0.
func TestTokensLoadedUnheldNonLiveRegistry(t *testing.T) {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "t"})
	g.Set(0)
	tokensLoadedUnheld(context.Background(), rivian.NewNopAccountRegistry(), perUserFencer{}, g)
	if got := testutil.ToFloat64(g); got != 0 {
		t.Fatalf("gauge = %v, want 0", got)
	}
}
