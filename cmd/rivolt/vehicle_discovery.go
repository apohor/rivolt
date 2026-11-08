package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/vehiclediscovery"
)

// startVehicleDiscovery runs the vehicle discovery job (internal/vehiclediscovery) on this pod; the job takes a Postgres advisory lock, so
// only one pod's pass does anything. It finds the linked users nobody records (a stored Rivian session, no vehicle row, so no group, no lease
// and no owner) and primes their vehicles with one lookup each. Single-owner M3-6.
func startVehicleDiscovery(ctx context.Context, logger *slog.Logger, appMetrics *metrics.Metrics, job *vehiclediscovery.Job) {
	for _, o := range []string{vehiclediscovery.OutcomeStored, vehiclediscovery.OutcomeEmpty, vehiclediscovery.OutcomeError, vehiclediscovery.OutcomeSkipped, vehiclediscovery.OutcomeNeedsReauth} {
		appMetrics.VehicleDiscoveryTotal.WithLabelValues(o).Add(0)
	}
	job.Observe = func(outcome string) { appMetrics.VehicleDiscoveryTotal.WithLabelValues(outcome).Inc() }
	job.Logger = logger
	go job.Run(ctx)
}

// discoveryLookup is the job's only Rivian call: the user's vehicle list, with the user's own session (restored from the secrets store when this
// pod's client has none, as the HTTP path's clientFor does). The account was built with the reauth sink, so a rejected session is flagged and
// the one-time email sent by the client itself.
func discoveryLookup(accounts rivian.AccountRegistry, store *secrets.Store) vehiclediscovery.Lookup {
	return func(ctx context.Context, uid uuid.UUID) ([]rivian.Vehicle, error) {
		a := accounts.For(uid)
		c, ok := a.(rivian.Client)
		if a == nil || !ok {
			return nil, errors.New("no Rivian client for the user")
		}
		if store != nil && !a.Authenticated() {
			// Detached from any request, so it carries the user explicitly: the secrets store is user-scoped under row-level security.
			sess, err := secrets.LoadRivianSession(auth.WithUser(ctx, uid), store, uid)
			if err == nil && sess.UserSessionToken != "" {
				a.Restore(sess)
			}
		}
		return c.Vehicles(ctx)
	}
}
