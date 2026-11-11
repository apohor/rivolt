package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

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
		return st == fence.Off || st == fence.Held || st == fence.Warming, true
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
