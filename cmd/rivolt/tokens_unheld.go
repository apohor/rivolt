package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

// tokensUnheldEvery is how often rivolt_tokens_loaded_unheld is recomputed: one group lookup per loaded account, so not often.
const tokensUnheldEvery = 60 * time.Second

// unheldCounter is the part of the live account registry that counts tokens held for users this pod does not own.
type unheldCounter interface {
	UnheldTokens(held func(uuid.UUID) bool) int
}

// holdsUser is the single-owner M4-5 hold predicate, the one reauthOwnerCtx uses: the pod holds the user's group (Held or Warming). With lead
// mode off (fence.Off) every pod holds everything and the gauge reads 0. Anything that cannot be told counts as not held.
func holdsUser(ctx context.Context, f userFencer, uid uuid.UUID) bool {
	_, st, err := f.FenceUser(ctx, uid.String())
	if err != nil {
		return false
	}
	return st == fence.Off || st == fence.Held || st == fence.Warming
}

// tokensLoadedUnheld sets g to the number of clients in reg that hold a session token for a user whose group this pod does not hold.
// A registry that is not the live one has no tokens to count and leaves g at 0.
func tokensLoadedUnheld(ctx context.Context, reg rivian.AccountRegistry, f userFencer, g prometheus.Gauge) {
	c, ok := reg.(unheldCounter)
	if !ok {
		return
	}
	g.Set(float64(c.UnheldTokens(func(uid uuid.UUID) bool {
		qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return holdsUser(qctx, f, uid)
	})))
}

// startTokensLoadedUnheld recomputes the gauge every tokensUnheldEvery until ctx ends.
func startTokensLoadedUnheld(ctx context.Context, reg rivian.AccountRegistry, f userFencer, g prometheus.Gauge, log *slog.Logger) {
	if _, ok := reg.(unheldCounter); !ok {
		return
	}
	go func() {
		t := time.NewTicker(tokensUnheldEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tokensLoadedUnheld(ctx, reg, f, g)
			}
		}
	}()
	log.Info("tokens-loaded-unheld gauge started", "every", tokensUnheldEvery.String())
}
