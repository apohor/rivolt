package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/rivian"
)

// noStandingTokensRequires are the settings RIVOLT_NO_STANDING_TOKENS=1 depends on (single-owner M4-5). Without each of them a pod that holds no token
// for a user has no way to serve that user, and the failure shows up as a quiet loss of service rather than at boot:
//   - SINGLE_OWNER: needs_reauth raises are the owner's, and the pod that sees a rejection is not.
//   - GROUP_LEAD: "held" means holding the group's lease; without lead mode every pod holds everything.
//   - FORWARD: a request on a pod that does not hold the group is answered by the one that does.
//   - OWNER_DISCOVERY: vehicle discovery runs on the owner, as a non-owner has no client to run it with.
//   - ACQUIRE_ON_REQUEST: without it the forwarder falls back to running the request on the pod itself (cmd/rivolt/forward.go), where there is no token.
//
// Permit enforcement is not required.
var noStandingTokensRequires = []string{
	"RIVOLT_SINGLE_OWNER",
	"RIVOLT_GROUP_LEAD",
	"RIVOLT_FORWARD",
	"RIVOLT_OWNER_DISCOVERY",
	"RIVOLT_ACQUIRE_ON_REQUEST",
}

// noStandingTokensError is nil with RIVOLT_NO_STANDING_TOKENS off, and with it on when everything it depends on is on; otherwise it names the first
// setting that is missing. main exits 1 on it.
func noStandingTokensError(getenv func(string) string) error {
	if getenv("RIVOLT_NO_STANDING_TOKENS") != "1" {
		return nil
	}
	for _, k := range noStandingTokensRequires {
		if getenv(k) != "1" {
			return fmt.Errorf("RIVOLT_NO_STANDING_TOKENS=1 needs %s=1", k)
		}
	}
	// The standalone public demo has no owner pods and no lease groups: nothing there could hold a user's group.
	if v := getenv("RIVOLT_DEMO_ONLY"); v == "true" || v == "1" {
		return errors.New("RIVOLT_NO_STANDING_TOKENS=1 cannot run on the standalone demo (RIVOLT_DEMO_ONLY)")
	}
	return nil
}

// tokensReleaseEvery is how often a pod wipes the clients of groups it has lost: it is the bound, with the lease deadline, on how long a token
// outlives the ownership.
const tokensReleaseEvery = 5 * time.Second

// groupHold is the registry's hold predicate (see holdsUser): held says this pod holds the user's group, known says the answer is one (an
// error from the lookup is not).
func groupHold(f userFencer) func(context.Context, uuid.UUID) (held, known bool) {
	return func(ctx context.Context, uid uuid.UUID) (bool, bool) {
		_, st, err := f.FenceUser(ctx, uid.String())
		if err != nil {
			return false, false
		}
		return st == fence.Held || st == fence.Warming, true
	}
}

// releaser is the part of the live registry that drops the clients of lost groups.
type releaser interface {
	ReleaseUnheld(ctx context.Context) int
}

// startTokenRelease wipes the clients of groups this pod has lost every tokensReleaseEvery until ctx ends.
func startTokenRelease(ctx context.Context, reg rivian.AccountRegistry, log *slog.Logger) {
	rel, ok := reg.(releaser)
	if !ok {
		return
	}
	go func() {
		t := time.NewTicker(tokensReleaseEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n := rel.ReleaseUnheld(ctx); n > 0 {
					log.Info("no standing tokens: wiped the clients of groups this pod no longer holds", "clients", n)
				}
			}
		}
	}()
}

// installNoStandingTokens gates reg when RIVOLT_NO_STANDING_TOKENS is on (the live and the replay registry share it). Lead mode is required, so
// Require goes on before the gate: until the lease store is installed in liveFence the Holder answers Lost, and groupHold counts only Held
// and Warming, so the boot window holds no token. bare builds ForLogin's throwaway client (live only).
func installNoStandingTokens(ctx context.Context, reg rivian.AccountRegistry, log *slog.Logger, bare func(uuid.UUID) *rivian.LiveClient) {
	if os.Getenv("RIVOLT_NO_STANDING_TOKENS") != "1" {
		return
	}
	liveFence.Require(true)
	g, ok := reg.(interface {
		SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool), func(uuid.UUID) *rivian.LiveClient)
	})
	if !ok {
		return
	}
	hold := groupHold(&liveFence)
	g.SetNoStandingTokens(func(ctx context.Context, uid uuid.UUID) (bool, bool) {
		// The sample-corpus demo account (preview, signed-out visitors at /demo) has no vehicle, so no group to hold, and no token.
		if demo.Enabled() && uid == demo.UserID() {
			return true, true
		}
		return hold(ctx, uid)
	}, bare)
	startTokenRelease(ctx, reg, log)
	log.Info("RIVOLT_NO_STANDING_TOKENS is on: Rivian session tokens are kept only for the groups this pod holds")
}
